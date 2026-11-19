package releaseperf

import "testing"

func sampleFingerprint() HostFingerprint {
	return HostFingerprint{
		CPUModel: "AMD Ryzen 9 7950X", LogicalCPUs: 32, MemoryGiB: 64,
		Kernel: "5.15.167.4-microsoft-standard-WSL2", WSL: true,
		Browser: "Chromium 131.0.6778.0",
	}
}

func TestHostFingerprintMismatchNoneWhenEqual(t *testing.T) {
	a, b := sampleFingerprint(), sampleFingerprint()
	if field, mismatched := a.Mismatch(b); mismatched {
		t.Fatalf("expected no mismatch, got field %q", field)
	}
}

func TestHostFingerprintMismatchNamesTheDifferingField(t *testing.T) {
	a := sampleFingerprint()
	b := sampleFingerprint()
	b.Kernel = "6.1.0-generic"
	field, mismatched := a.Mismatch(b)
	if !mismatched {
		t.Fatal("expected a mismatch")
	}
	if field != "kernel" {
		t.Fatalf("expected the mismatch to name \"kernel\", got %q", field)
	}
}

func TestHostFingerprintMismatchOnBrowser(t *testing.T) {
	a := sampleFingerprint()
	b := sampleFingerprint()
	b.Browser = "Firefox 133.0"
	field, mismatched := a.Mismatch(b)
	if !mismatched || field != "browser" {
		t.Fatalf("expected a browser mismatch, got field=%q mismatched=%v", field, mismatched)
	}
}

func TestReadHostFingerprintPopulatesRealFields(t *testing.T) {
	fp, err := ReadHostFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if fp.LogicalCPUs <= 0 {
		t.Fatalf("expected a positive logical CPU count, got %d", fp.LogicalCPUs)
	}
	if fp.Kernel == "" {
		t.Fatal("expected a non-empty kernel string")
	}
	if fp.MemoryGiB <= 0 {
		t.Fatalf("expected positive total memory, got %v", fp.MemoryGiB)
	}
}
