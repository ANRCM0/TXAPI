package generation

import (
	"context"
	"testing"

	execv1 "minigateway/internal/foundation/contracts/execution/v1"
)

func TestPublicTypesReadReleasedEncryptedRecording(t *testing.T) {
	st, binding, _ := fixture(t)
	opts := archiveOptions(st)
	archive := testArchive(t, opts)
	record, err := archive.start(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	// Released archive bytes are fixtures rather than values marshaled from the
	// new contract types, so the test detects accidental persisted-format changes.
	for _, segment := range []struct{ kind, data string }{
		{"metadata", `{"StatusCode":200,"ContentType":"application/json","Values":{"X-TXAPI-Price-Version":"released-price"}}`},
		{"body", `released private output`},
		{"result", `{"Operation":"responses","Model":"m","StatusCode":502,"Phase":"response_received","Termination":"client_write_error","Committed":true,"ProtocolComplete":false,"BillingState":"pending","Reason":"observed"}`},
	} {
		if err := record.append(context.Background(), segment.kind, []byte(segment.data)); err != nil {
			t.Fatal(err)
		}
	}
	fresh := testArchive(t, opts)
	retained, err := fresh.Read(context.Background(), binding, record.record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retained.Metadata.Values["X-TXAPI-Price-Version"] != "released-price" || string(retained.Body) != "released private output" || retained.Result.Operation != execv1.Responses || retained.Result.Phase != execv1.ResponseReceived || retained.Result.Termination != execv1.ClientWriteError || retained.Result.ProtocolComplete || retained.Result.BillingState != "pending" {
		t.Fatalf("released data semantics changed: %+v", retained)
	}
}
