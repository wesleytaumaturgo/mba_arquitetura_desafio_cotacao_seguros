package quotation

import "testing"

func TestNormalizeFillsDefaultsAndTidiesUp(t *testing.T) {
	request := Request{
		Driver:  Driver{Document: "  12345678901 ", BirthYear: 1988},
		Vehicle: Vehicle{Plate: " abc1d23 ", Model: " Gol 1.0 ", Year: 2020, ValueCents: 8500000},
	}

	if err := request.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if request.Coverage != defaultCoverage {
		t.Errorf("coverage %q, expected %q", request.Coverage, defaultCoverage)
	}
	if request.Vehicle.Plate != "ABC1D23" {
		t.Errorf("plate %q, expected ABC1D23", request.Vehicle.Plate)
	}
	if request.Driver.Document != "12345678901" {
		t.Errorf("document %q, expected no surrounding spaces", request.Driver.Document)
	}
}

func TestNormalizeRejectsIncompleteRequest(t *testing.T) {
	complete := func() Request {
		return Request{
			Driver:  Driver{Document: "12345678901", BirthYear: 1988},
			Vehicle: Vehicle{Plate: "ABC1D23", Year: 2020, ValueCents: 8500000},
		}
	}

	cases := map[string]func(*Request){
		"missing document":     func(r *Request) { r.Driver.Document = "" },
		"missing birth year":   func(r *Request) { r.Driver.BirthYear = 0 },
		"missing plate":        func(r *Request) { r.Vehicle.Plate = "" },
		"missing vehicle year": func(r *Request) { r.Vehicle.Year = 0 },
		"zero value":           func(r *Request) { r.Vehicle.ValueCents = 0 },
		"negative value":       func(r *Request) { r.Vehicle.ValueCents = -1 },
		"invalid coverage":     func(r *Request) { r.Coverage = "vip" },
	}

	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			request := complete()
			breakIt(&request)
			if err := request.Normalize(); err == nil {
				t.Fatalf("invalid request was accepted: %+v", request)
			}
		})
	}
}

func normalizedRequest(t *testing.T) Request {
	t.Helper()
	r := Request{
		Driver:  Driver{Document: "12345678901", BirthYear: 1988},
		Vehicle: Vehicle{Plate: "ABC1D23", Model: "Gol 1.0", Year: 2020, ValueCents: 8500000},
	}
	if err := r.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	return r
}

func TestFingerprintIsStableForTheSameNormalizedRequest(t *testing.T) {
	a, b := normalizedRequest(t), normalizedRequest(t)

	if a.Fingerprint() != b.Fingerprint() {
		t.Errorf("fingerprints differ for identical normalized requests: %q vs %q", a.Fingerprint(), b.Fingerprint())
	}
}

func TestFingerprintChangesWhenAnyFieldChanges(t *testing.T) {
	baseline := normalizedRequest(t).Fingerprint()

	cases := map[string]func(*Request){
		"document":    func(r *Request) { r.Driver.Document = "98765432100" },
		"birth year":  func(r *Request) { r.Driver.BirthYear = 1990 },
		"plate":       func(r *Request) { r.Vehicle.Plate = "XYZ9A88" },
		"model":       func(r *Request) { r.Vehicle.Model = "Onix" },
		"year":        func(r *Request) { r.Vehicle.Year = 2021 },
		"value cents": func(r *Request) { r.Vehicle.ValueCents = 9000000 },
		"coverage":    func(r *Request) { r.Coverage = "third_party" },
	}

	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r := normalizedRequest(t)
			change(&r)
			if got := r.Fingerprint(); got == baseline {
				t.Errorf("changing %s did not change the fingerprint (%q)", name, got)
			}
		})
	}
}
