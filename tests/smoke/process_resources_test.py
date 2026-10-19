"""Controlled OS observations, not resource limits for the production gateway."""
import gc
import ctypes
import json
import os
import platform
import subprocess
import sys
import unittest
from unittest.mock import patch

from process_resources import Resources, resource


@unittest.skipUnless(platform.system() in ("Windows", "Linux"), "collector not supported")
class ProcessResourceTests(unittest.TestCase):
    def test_cpu_memory_and_open_objects_track_separate_live_process(self):
        script = r'''
import gc, json, os, sys, time
print("ready", flush=True)
for command in sys.stdin:
    if command.strip() == "allocate":
        memory = bytearray(32 * 1024 * 1024)
        for index in range(0, len(memory), 4096): memory[index] = 1
        files = [open(os.devnull, "rb") for _ in range(64)]
        until = time.process_time() + .2
        while time.process_time() < until: pass
    elif command.strip() == "release":
        for file in files: file.close()
        del files, memory
        gc.collect()
    print("ready", flush=True)
'''
        process = subprocess.Popen([sys.executable, "-u", "-c", script],
                                   stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
        try:
            self.assertEqual(process.stdout.readline().strip(), "ready")
            before = resource(process.pid)
            self.assertEqual(before["status"], "available", before)
            process.stdin.write("allocate\n")
            process.stdin.flush()
            self.assertEqual(process.stdout.readline().strip(), "ready")
            loaded = resource(process.pid)
            self.assertEqual(loaded["status"], "available", loaded)
            self.assertEqual(before["process_identity"], loaded["process_identity"])
            self.assertGreaterEqual(loaded["cpu_seconds"] - before["cpu_seconds"], .1)
            self.assertGreaterEqual(loaded["rss_bytes"] - before["rss_bytes"], 16 * 1024 * 1024)
            self.assertGreaterEqual(loaded["process_lifetime_peak_rss_bytes"], loaded["rss_bytes"])
            count = "handle_count" if platform.system() == "Windows" else "fd_count"
            self.assertGreaterEqual(loaded[count] - before[count], 60)
            if platform.system() == "Windows":
                self.assertGreaterEqual(loaded["private_commit_bytes"] - before["private_commit_bytes"], 24 * 1024 * 1024)
                self.assertNotIn("fd_count", loaded)
                self.assertNotIn("threads", loaded)
            else:
                self.assertGreaterEqual(loaded["threads"], 1)
                self.assertNotIn("handle_count", loaded)
            process.stdin.write("release\n")
            process.stdin.flush()
            self.assertEqual(process.stdout.readline().strip(), "ready")
            released = resource(process.pid)
            self.assertEqual(released["status"], "available", released)
            self.assertEqual(released[count], before[count])
        finally:
            process.stdin.close()
            process.wait(timeout=5)
            process.stdout.close()
        self.assertEqual(process.returncode, 0)
        self.assertEqual(resource(process.pid)["status"], "unavailable")

    def test_repeated_sampling_closes_query_handles(self):
        count = "handle_count" if platform.system() == "Windows" else "fd_count"
        resource(os.getpid())  # Warm lazy Windows DLL/function binding.
        gc.collect()
        before = resource(os.getpid())
        self.assertEqual(before["status"], "available", before)
        for _ in range(100):
            value = resource(os.getpid())
            self.assertEqual(value["status"], "available", value)
            self.assertEqual(value["process_identity"], before["process_identity"])
        gc.collect()
        after = resource(os.getpid())
        self.assertEqual(before[count], after[count])

    @unittest.skipUnless(platform.system() == "Windows", "Windows API failure cleanup")
    def test_query_failures_also_close_the_process_handle(self):
        from process_resources import windows_api
        api, _ = windows_api()
        def denied(*args):
            ctypes.set_last_error(5)  # ERROR_ACCESS_DENIED
            return 0
        for name in ("GetExitCodeProcess", "GetProcessTimes", "K32GetProcessMemoryInfo", "GetProcessHandleCount"):
            before = resource(os.getpid())
            with patch.object(api, name, side_effect=denied):
                for _ in range(10):
                    value = resource(os.getpid())
                    self.assertEqual(value["status"], "unavailable", name)
                    self.assertEqual(value["os_error"], 5)
                    self.assertNotIn("rss_bytes", value)
            after = resource(os.getpid())
            self.assertEqual(before["handle_count"], after["handle_count"], name)

    def test_invalid_pid_is_unavailable_without_measurements(self):
        for pid in (0, -1, 2**32, "1"):
            value = resource(pid)
            self.assertEqual(value["status"], "unavailable")
            self.assertNotIn("cpu_seconds", value)
            self.assertNotIn("rss_bytes", value)

    def test_live_sampler_joins_and_includes_endpoints_in_peaks(self):
        sampler = Resources(os.getpid())
        value = sampler.finish()
        self.assertFalse(sampler.thread.is_alive())
        for name in value["before"]:
            self.assertEqual(value["before"][name]["status"], "available")
            self.assertEqual(value["after_drain"][name]["status"], "available")
            self.assertGreaterEqual(value["cpu_delta_seconds"][name], 0)
            self.assertGreaterEqual(value["sampling"][name]["attempted"], 2)
            for key, peak in value["sampled_peaks"][name].items():
                self.assertGreaterEqual(peak, value["before"][name][key])
                self.assertGreaterEqual(peak, value["after_drain"][name][key])
        json.dumps(value)


class MissingObservationTests(unittest.TestCase):
    def test_failure_and_pid_reuse_preserve_gaps_without_mixing_peaks(self):
        value = {"status": "available", "collector": "test", "process_identity": "original",
                 "cpu_seconds": 2, "rss_bytes": 4}
        with patch("process_resources.resource", return_value=value):
            sampler = Resources(11)
            sampler.stop.set()
            sampler.thread.join()
            sampler.observe("gateway", {"status": "unavailable", "reason": "PermissionError"})
            reused = value | {"process_identity": "replacement", "rss_bytes": 999, "cpu_seconds": 100}
            with patch("process_resources.resource", return_value=reused):
                result = sampler.finish()
        self.assertEqual(result["sampled_peaks"]["gateway"]["rss_bytes"], 4)
        self.assertEqual(result["cpu_delta_seconds"]["gateway"], "unavailable")
        self.assertEqual(result["after_drain"]["gateway"]["reason"], "process_identity_changed")
        self.assertEqual(result["sampling"]["gateway"]["reasons"],
                         {"PermissionError": 1, "process_identity_changed": 1})

    def test_unsupported_does_not_turn_into_zero_cpu_or_memory(self):
        with patch("process_resources.platform.system", return_value="unimplemented"):
            sampler = Resources(11)
            result = sampler.finish()
        self.assertEqual(result["cpu_delta_seconds"]["gateway"], "unsupported")
        self.assertEqual(result["sampled_peaks"]["gateway"], {})


if __name__ == "__main__":
    unittest.main(verbosity=2)
