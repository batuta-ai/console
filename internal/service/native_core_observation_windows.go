package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/batuta-ai/console/internal/session"
)

const nativeCoreObservationFlag = "BATUTA_PRIVATE_NATIVE_CORE_OBSERVATION"
const nativeCoreObservationValue = "never-ship-v1"
const nativeCoreObservationMetadata = "BATUTA_PRIVATE_NATIVE_CORE_OBSERVATION_METADATA"

// Sample once at process start. Ambient changes cannot enable a running host.
var nativeCoreObservationEnabled = os.Getenv(nativeCoreObservationFlag) == nativeCoreObservationValue
var nativeCoreObservationOperations atomic.Uint64

type nativeCoreObservation struct {
	*session.NativeExchangeDiagnostic
	file     *os.File
	metadata string
}

func newNativeCoreObservation(helper bool) *nativeCoreObservation {
	if !nativeCoreObservationEnabled {
		return nil
	}
	count := nativeCoreObservationOperations.Add(1)
	if count > 512 {
		if count == 513 {
			directory := filepath.Join(os.TempDir(), "batuta-native-core-observation")
			_ = os.WriteFile(filepath.Join(directory, "native-core-observation-limit.jsonl"), []byte("{\"phase\":\"capture.operations.exhausted\",\"limit\":512}\n"), 0600)
		}
		return nil
	}
	operation, directory, side := "", "", "parent"
	if helper {
		metadata := os.Getenv(nativeCoreObservationMetadata)
		// Only this private helper consumes these entries; Core/provider launches
		// must not inherit the diagnostic switch or metadata, even via os.Environ.
		_ = os.Unsetenv(nativeCoreObservationFlag)
		_ = os.Unsetenv(nativeCoreObservationMetadata)
		operation, directory, _ = strings.Cut(metadata, "|")
		side = "helper"
		decoded, err := hex.DecodeString(operation)
		if err != nil || len(decoded) != 16 || !filepath.IsAbs(directory) {
			return nil
		}
	} else {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return nil
		}
		operation = hex.EncodeToString(id[:])
		directory = filepath.Join(os.TempDir(), "batuta-native-core-observation")
		if err := os.MkdirAll(directory, 0700); err != nil {
			return nil
		}
	}
	file, err := os.OpenFile(filepath.Join(directory, "native-core-observation-"+operation+"-"+side+".jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil
	}
	o := &nativeCoreObservation{NativeExchangeDiagnostic: session.NewNativeExchangeDiagnostic(file, operation, side), file: file, metadata: operation + "|" + directory}
	o.Observe("capture.begin", "", 0, nil, 1)
	return o
}

func (o *nativeCoreObservation) close() {
	if o == nil {
		return
	}
	o.Finish()
	_ = o.file.Close()
}

func (o *nativeCoreObservation) context(ctx context.Context) context.Context {
	if o == nil {
		return ctx
	}
	return session.WithNativeExchangeDiagnostic(ctx, o.NativeExchangeDiagnostic, o.metadata)
}

func (o *nativeCoreObservation) observer() *session.NativeExchangeDiagnostic {
	if o == nil {
		return nil
	}
	return o.NativeExchangeDiagnostic
}
