package telephony

// simphonebook.go — reading the box SIM's own stored phonebook (the contacts
// saved ON the SIM card), so a box with a GSM modem can contribute those entries
// to the unified Contacts address book.
//
// STATUS — best-effort seam, OFF by default:
//
// ModemManager's mmcli exposes SMS + the SIM object (ICCID/operator) but has NO
// stable, cross-version command to read the SIM's stored phonebook. The only way
// to reach it is the modem's raw AT interface (`AT+CPBS="SM"` to select the SIM
// phonebook, then `AT+CPBR=1,N` to read the entries), which mmcli can pass through
// with `--command=` — but ONLY when ModemManager was started in debug mode
// (`--debug`), otherwise the daemon refuses AT passthrough.
//
// Because AT passthrough is unavailable on a normally-configured box, this reader
// DEGRADES GRACEFULLY: SIMPhonebook() returns (nil, false) unless the operator has
// explicitly opted in via VULOS_TELEPHONY_SIM_PHONEBOOK=1 AND the modem answers
// the AT read. Callers must treat the bool as "is this source active" and simply
// omit the box-sim source when it is false — nothing depends on it.

import (
	"os"
	"regexp"
	"strconv"
	"strings"
)

// SIMContact is one entry read from the SIM card's stored phonebook.
type SIMContact struct {
	Name   string `json:"name"`
	Number string `json:"number"`
}

// cpbrLine matches an AT+CPBR entry: `+CPBR: <index>,"<number>",<type>,"<text>"`.
// Numbers may contain a leading + and digits; the text is the stored name.
var cpbrLine = regexp.MustCompile(`\+CPBR:\s*\d+,"([^"]*)",\d+,"([^"]*)"`)

// simPhonebookEnabled reports whether the operator opted this box into the
// AT-passthrough SIM-phonebook read. Default (unset) is OFF — the feature needs
// ModemManager in debug mode, so we never attempt it silently.
func simPhonebookEnabled() bool {
	v := strings.TrimSpace(os.Getenv("VULOS_TELEPHONY_SIM_PHONEBOOK"))
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
}

// SIMPhonebook reads the contacts stored on the SIM card, best-effort.
//
// The bool is the SOURCE-ACTIVE flag: true only when the read actually succeeded
// (opt-in set, modem present, AT passthrough answered). On any failure — feature
// off, no modem, AT refused, nothing stored — it returns (nil, false) so the
// unified address book simply omits the box-sim source. It never errors.
func (s *Service) SIMPhonebook() ([]SIMContact, bool) {
	if !simPhonebookEnabled() {
		return nil, false
	}
	id := s.modemIndex()
	if id == "" {
		return nil, false
	}
	// Select the SIM ("SM") phonebook. If the modem rejects passthrough (debug
	// mode off) this errors and we degrade to inactive.
	if _, err := mmcli("-m", id, `--command=AT+CPBS="SM"`); err != nil {
		return nil, false
	}
	out, err := mmcli("-m", id, "--command=AT+CPBR=1,250")
	if err != nil {
		return nil, false
	}
	contacts := parseCPBR(out)
	// A successful read of an EMPTY phonebook is still an active source (the SIM
	// simply has no saved contacts), so report active=true when the AT calls
	// succeeded. Distinguish that from the no-modem/refused case above.
	return contacts, true
}

// parseCPBR turns raw AT+CPBR output into SIM contacts. Split out for tests.
// Entries with neither a name nor a number are dropped; a name-only or
// number-only entry is kept (the merge layer copes with sparse cards).
func parseCPBR(out string) []SIMContact {
	var res []SIMContact
	for _, m := range cpbrLine.FindAllStringSubmatch(out, -1) {
		number := strings.TrimSpace(m[1])
		name := decodeCPBRText(strings.TrimSpace(m[2]))
		if number == "" && name == "" {
			continue
		}
		res = append(res, SIMContact{Name: name, Number: number})
	}
	return res
}

// decodeCPBRText best-effort decodes the CPBR text field. Modems commonly store
// names as UCS2 hex (a run of 4-hex-digit code units) when +CSCS="UCS2"; if the
// text is a clean even-length hex string it is decoded, otherwise it is returned
// verbatim (GSM/ASCII names pass straight through).
func decodeCPBRText(s string) string {
	if s == "" || len(s)%4 != 0 || !isHex(s) {
		return s
	}
	var b strings.Builder
	for i := 0; i+4 <= len(s); i += 4 {
		v, err := strconv.ParseUint(s[i:i+4], 16, 32)
		if err != nil {
			return s
		}
		b.WriteRune(rune(v))
	}
	return b.String()
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}
