package deploymode

import "testing"

// KEYSTORE-CUSTODY-01: a cloud-managed box must refuse the plaintext software
// keystore unless the operator explicitly opts out; standalone self-host may
// always use it.
func TestRefuseSoftwareKeystore(t *testing.T) {
	cases := []struct {
		name       string
		mode       Mode
		software   bool
		optOut     bool
		wantRefuse bool
	}{
		// Managed (OS) + software fallback + no opt-out => REFUSE.
		{"os_software_no_optout", OS, true, false, true},
		{"cloud_software_no_optout", Cloud, true, false, true},

		// Managed + TPM/hardware => OK.
		{"os_hardware", OS, false, false, false},
		{"cloud_hardware", Cloud, false, false, false},

		// Managed + software + explicit opt-out => OK (auditable acknowledgement).
		{"os_software_optout", OS, true, true, false},
		{"cloud_software_optout", Cloud, true, true, false},

		// Standalone self-host + software => always OK (legitimate fallback).
		{"standalone_software", Standalone, true, false, false},
		{"standalone_software_optout", Standalone, true, true, false},
		{"standalone_hardware", Standalone, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.mode.RefuseSoftwareKeystore(c.software, c.optOut); got != c.wantRefuse {
				t.Fatalf("%s.RefuseSoftwareKeystore(software=%v, optOut=%v) = %v, want %v",
					c.mode, c.software, c.optOut, got, c.wantRefuse)
			}
		})
	}
}
