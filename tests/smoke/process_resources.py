"""Read-only process samples for the controlled baseline harnesses.

Windows API layouts/units follow Microsoft Learn's GetProcessTimes,
GetProcessHandleCount and PROCESS_MEMORY_COUNTERS_EX documentation. Working
set includes shared resident pages; PrivateUsage is private commit, not RSS.
"""
import ctypes
from functools import lru_cache
import os
from pathlib import Path
import platform
import threading


RESOURCE_SCOPE = (
    "Linux /proc or Windows Kernel32 read-only process queries; other platforms unsupported; "
    "harness clients, fixture and sampler share PID; sampling overhead is included; 50ms sampled peaks are lower bounds; "
    "Windows working set is resident memory, private commit is not RSS, handles are not FDs; "
    "Windows threads/FDs and Go goroutines unmeasured; harness handle count includes one "
    "temporary query handle; Linux self FD enumeration includes its directory descriptor; "
    "process-lifetime memory peaks are separate from phase peaks"
)
PEAK_METRICS = ("rss_bytes", "threads", "fd_count", "handle_count", "private_commit_bytes")


@lru_cache(maxsize=1)
def windows_api():
    from ctypes import wintypes

    class MemoryCounters(ctypes.Structure):
        _fields_ = [("cb", wintypes.DWORD), ("PageFaultCount", wintypes.DWORD)] + [
            (name, ctypes.c_size_t) for name in (
                "PeakWorkingSetSize", "WorkingSetSize", "QuotaPeakPagedPoolUsage",
                "QuotaPagedPoolUsage", "QuotaPeakNonPagedPoolUsage", "QuotaNonPagedPoolUsage",
                "PagefileUsage", "PeakPagefileUsage", "PrivateUsage")]

    api = ctypes.WinDLL("kernel32", use_last_error=True)
    declarations = {
        "OpenProcess": ([wintypes.DWORD, wintypes.BOOL, wintypes.DWORD], wintypes.HANDLE),
        "CloseHandle": ([wintypes.HANDLE], wintypes.BOOL),
        "GetExitCodeProcess": ([wintypes.HANDLE, ctypes.POINTER(wintypes.DWORD)], wintypes.BOOL),
        "GetProcessTimes": ([wintypes.HANDLE] + [ctypes.POINTER(wintypes.FILETIME)] * 4, wintypes.BOOL),
        "GetProcessHandleCount": ([wintypes.HANDLE, ctypes.POINTER(wintypes.DWORD)], wintypes.BOOL),
        "K32GetProcessMemoryInfo": ([wintypes.HANDLE, ctypes.POINTER(MemoryCounters), wintypes.DWORD], wintypes.BOOL),
    }
    for name, (arguments, result) in declarations.items():
        function = getattr(api, name)
        function.argtypes, function.restype = arguments, result
    return api, MemoryCounters


def windows_resource(pid):
    from ctypes import wintypes
    api, counters_type = windows_api()
    handle = api.OpenProcess(0x1000, False, pid)  # PROCESS_QUERY_LIMITED_INFORMATION
    if not handle:
        raise ctypes.WinError(ctypes.get_last_error())
    try:
        exit_code = wintypes.DWORD()
        if not api.GetExitCodeProcess(handle, ctypes.byref(exit_code)):
            raise ctypes.WinError(ctypes.get_last_error())
        if exit_code.value != 259:  # STILL_ACTIVE
            return {"status": "unavailable", "reason": "process_exited"}
        creation, exit_time, kernel, user = [wintypes.FILETIME() for _ in range(4)]
        if not api.GetProcessTimes(handle, ctypes.byref(creation), ctypes.byref(exit_time),
                                   ctypes.byref(kernel), ctypes.byref(user)):
            raise ctypes.WinError(ctypes.get_last_error())
        counters, handles = counters_type(), wintypes.DWORD()
        counters.cb = ctypes.sizeof(counters)
        if not api.K32GetProcessMemoryInfo(handle, ctypes.byref(counters), counters.cb):
            raise ctypes.WinError(ctypes.get_last_error())
        if not api.GetProcessHandleCount(handle, ctypes.byref(handles)):
            raise ctypes.WinError(ctypes.get_last_error())
        def ticks(value):
            return (value.dwHighDateTime << 32) | value.dwLowDateTime
        return {"status": "available", "collector": "windows_kernel32",
                "process_identity": str(ticks(creation)),
                "cpu_seconds": (ticks(kernel) + ticks(user)) / 10_000_000,
                "rss_bytes": counters.WorkingSetSize,
                "process_lifetime_peak_rss_bytes": counters.PeakWorkingSetSize,
                "private_commit_bytes": counters.PrivateUsage, "handle_count": handles.value}
    finally:
        if not api.CloseHandle(handle):
            raise ctypes.WinError(ctypes.get_last_error())


def resource(pid):
    system = platform.system()
    if system not in ("Linux", "Windows"):
        return {"status": "unsupported", "platform": system}
    if not isinstance(pid, int) or not 0 < pid <= 0xffffffff:
        return {"status": "unavailable", "reason": "invalid_pid"}
    try:
        if system == "Windows":
            return windows_resource(pid)
        proc = Path("/proc") / str(pid)
        def stat():
            # comm may contain spaces or parentheses; fields start after last ).
            return (proc / "stat").read_text().rsplit(")", 1)[1].split()
        fields = stat()
        status = dict(line.split(":", 1) for line in (proc / "status").read_text().splitlines())
        result = {"status": "available", "collector": "linux_proc", "process_identity": fields[19],
                  "cpu_seconds": (int(fields[11]) + int(fields[12])) / os.sysconf("SC_CLK_TCK"),
                  "rss_bytes": int(status["VmRSS"].split()[0]) * 1024,
                  "process_lifetime_peak_rss_bytes": int(status["VmHWM"].split()[0]) * 1024,
                  "threads": int(status["Threads"]), "fd_count": len(list((proc / "fd").iterdir()))}
        if fields[19] != stat()[19]:
            return {"status": "unavailable", "reason": "process_identity_changed"}
        return result
    except (OSError, KeyError, ValueError, IndexError, AttributeError) as error:
        return {"status": "unavailable", "reason": type(error).__name__,
                "os_error": getattr(error, "winerror", None) or getattr(error, "errno", None)}


class Resources:
    def __init__(self, gateway_pid):
        self.pids = {"gateway": gateway_pid, "harness_and_fixture": os.getpid()}
        self.identities = {}
        self.peaks = {name: {} for name in self.pids}
        self.sampling = {name: {"attempted": 0, "available": 0, "unavailable": 0,
                                "unsupported": 0, "reasons": {}} for name in self.pids}
        self.before = {name: self.observe(name, resource(pid)) for name, pid in self.pids.items()}
        self.stop = threading.Event()
        self.thread = threading.Thread(target=self.sample, daemon=True)
        self.thread.start()

    def observe(self, name, value):
        if value["status"] == "available":
            identity = (value["collector"], value["process_identity"])
            if self.identities.setdefault(name, identity) != identity:
                value = {"status": "unavailable", "reason": "process_identity_changed"}
        counts = self.sampling[name]
        counts["attempted"] += 1
        counts[value["status"]] += 1
        if value["status"] == "available":
            for key in PEAK_METRICS:
                if key in value:
                    self.peaks[name][key] = max(self.peaks[name].get(key, 0), value[key])
        else:
            reason = value.get("reason", value["status"])
            if reason not in counts["reasons"] and len(counts["reasons"]) >= 8:
                reason = "other"
            counts["reasons"][reason] = counts["reasons"].get(reason, 0) + 1
        return value

    def sample(self):
        while not self.stop.wait(.05):
            for name, pid in self.pids.items():
                self.observe(name, resource(pid))

    def finish(self):
        self.stop.set()
        self.thread.join()  # Queries are synchronous; no sampler may outlive its phase.
        after = {name: self.observe(name, resource(pid)) for name, pid in self.pids.items()}
        result = {"before": self.before, "sampled_peaks": self.peaks, "after_drain": after,
                  "sampling_interval_ms": 50, "sampling": self.sampling, "cpu_delta_seconds": {}}
        for name in self.pids:
            before, end = self.before[name], after[name]
            if before["status"] == end["status"] == "available":
                delta = end["cpu_seconds"] - before["cpu_seconds"]
                result["cpu_delta_seconds"][name] = delta if delta >= 0 else "unavailable"
            else:
                result["cpu_delta_seconds"][name] = (
                    "unsupported" if "unsupported" in (before["status"], end["status"]) else "unavailable")
        return result
