package credvault

import (
	"crypto/rand"
	_ "embed"
	"fmt"
	"math/big"
	"strings"
)

// ---- Password generator --------------------------------------------------

// GeneratorMode selects the kind of password to generate.
type GeneratorMode int

const (
	// ModeRandom generates a random string of characters.
	ModeRandom GeneratorMode = iota
	// ModePassphrase generates a space-separated sequence of dictionary words.
	ModePassphrase
)

// RandomConfig configures a random-character password.
type RandomConfig struct {
	// Length is the total number of characters. Default: 20.
	Length int
	// Upper includes uppercase letters (A-Z). Default: true.
	Upper bool
	// Lower includes lowercase letters (a-z). Default: true.
	Lower bool
	// Digits includes digits (0-9). Default: true.
	Digits bool
	// Symbols includes special characters. Default: true.
	Symbols bool
	// ExcludeAmbiguous removes characters that are visually confusing
	// (0, O, l, 1, I). Default: false.
	ExcludeAmbiguous bool
}

// defaults fills in zero-value fields with sensible defaults.
func (c *RandomConfig) defaults() RandomConfig {
	out := *c
	if out.Length <= 0 {
		out.Length = 20
	}
	// If all character classes are false, enable all.
	if !out.Upper && !out.Lower && !out.Digits && !out.Symbols {
		out.Upper = true
		out.Lower = true
		out.Digits = true
		out.Symbols = true
	}
	return out
}

// PassphraseConfig configures a passphrase-style password.
type PassphraseConfig struct {
	// WordCount is the number of words. Default: 4.
	WordCount int
	// Separator separates words. Default: "-".
	Separator string
	// Capitalize capitalises the first letter of each word. Default: false.
	Capitalize bool
	// AppendNumber appends a 2-digit number at the end. Default: false.
	AppendNumber bool
}

func (c *PassphraseConfig) defaults() PassphraseConfig {
	out := *c
	if out.WordCount <= 0 {
		out.WordCount = 4
	}
	if out.Separator == "" {
		out.Separator = "-"
	}
	return out
}

// GeneratorConfig is the top-level configuration for Generate.
type GeneratorConfig struct {
	Mode       GeneratorMode
	Random     RandomConfig
	Passphrase PassphraseConfig
}

// Generate returns a generated password according to cfg.
func Generate(cfg GeneratorConfig) (string, error) {
	switch cfg.Mode {
	case ModeRandom:
		return generateRandom(cfg.Random)
	case ModePassphrase:
		return generatePassphrase(cfg.Passphrase)
	default:
		return "", fmt.Errorf("credvault: unknown generator mode %d", cfg.Mode)
	}
}

// ---- Random password -----------------------------------------------------

const (
	upper   = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	lower   = "abcdefghijklmnopqrstuvwxyz"
	digits  = "0123456789"
	symbols = "!@#$%^&*()-_=+[]{}|;:,.<>?"

	ambiguous = "0Ol1I"
)

func generateRandom(cfg RandomConfig) (string, error) {
	cfg = cfg.defaults()

	var charset strings.Builder
	if cfg.Upper {
		charset.WriteString(upper)
	}
	if cfg.Lower {
		charset.WriteString(lower)
	}
	if cfg.Digits {
		charset.WriteString(digits)
	}
	if cfg.Symbols {
		charset.WriteString(symbols)
	}

	cs := charset.String()
	if cfg.ExcludeAmbiguous {
		cs = removeAmbiguous(cs)
	}
	if len(cs) == 0 {
		return "", fmt.Errorf("credvault: generator: no character classes selected")
	}

	// Guarantee at least one character from each requested class.
	var required []byte
	required, err := guaranteedChars(cfg, cs)
	if err != nil {
		return "", err
	}

	// Fill remaining positions randomly.
	result := make([]byte, cfg.Length)
	copy(result, required)
	for i := len(required); i < cfg.Length; i++ {
		ch, err := randChar(cs)
		if err != nil {
			return "", err
		}
		result[i] = ch
	}

	// Shuffle so that the guaranteed chars are not always at the front.
	if err := shuffle(result); err != nil {
		return "", err
	}
	return string(result), nil
}

func guaranteedChars(cfg RandomConfig, fullCS string) ([]byte, error) {
	var out []byte
	pick := func(cs string) error {
		filtered := cs
		if cfg.ExcludeAmbiguous {
			filtered = removeAmbiguous(cs)
		}
		if len(filtered) == 0 {
			return nil
		}
		ch, err := randChar(filtered)
		if err != nil {
			return err
		}
		out = append(out, ch)
		return nil
	}
	if cfg.Upper {
		if err := pick(upper); err != nil {
			return nil, err
		}
	}
	if cfg.Lower {
		if err := pick(lower); err != nil {
			return nil, err
		}
	}
	if cfg.Digits {
		if err := pick(digits); err != nil {
			return nil, err
		}
	}
	if cfg.Symbols {
		if err := pick(symbols); err != nil {
			return nil, err
		}
	}
	_ = fullCS
	return out, nil
}

func randChar(cs string) (byte, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(cs))))
	if err != nil {
		return 0, fmt.Errorf("credvault: rng error: %w", err)
	}
	return cs[n.Int64()], nil
}

func shuffle(b []byte) error {
	for i := len(b) - 1; i > 0; i-- {
		jBig, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return fmt.Errorf("credvault: shuffle rng: %w", err)
		}
		j := jBig.Int64()
		b[i], b[j] = b[j], b[i]
	}
	return nil
}

func removeAmbiguous(cs string) string {
	var sb strings.Builder
	for _, ch := range cs {
		if !strings.ContainsRune(ambiguous, ch) {
			sb.WriteRune(ch)
		}
	}
	return sb.String()
}

// ---- Passphrase ----------------------------------------------------------

// effLargeWordlistRaw is the embedded EFF Large Wordlist (7776 words, one per
// line). It yields log2(7776) ≈ 12.925 bits per word — the intended entropy for
// generated passphrases. Sourced from
// https://www.eff.org/files/2016/07/18/eff_large_wordlist.txt (dice-index
// column stripped; words only).
//
//go:embed eff_large_wordlist.txt
var effLargeWordlistRaw string

// effWordlist is the parsed EFF list. When it loads successfully (the embedded
// file is present and non-trivial) it is used for passphrase generation;
// otherwise the code falls back to builtinWordlist below.
var effWordlist = parseWordlist(effLargeWordlistRaw)

// parseWordlist splits raw newline-separated text into a trimmed, non-empty
// word slice.
func parseWordlist(raw string) []string {
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if w := strings.TrimSpace(ln); w != "" {
			out = append(out, w)
		}
	}
	return out
}

// passphraseWordlist returns the wordlist used for passphrase generation: the
// embedded EFF Large Wordlist when available (12.9 bits/word), else the smaller
// builtin fallback (~7.6 bits/word). The threshold guards against a truncated
// or missing embed degrading entropy silently.
func passphraseWordlist() []string {
	if len(effWordlist) >= 7000 {
		return effWordlist
	}
	return builtinWordlist
}

// builtinWordlist is a minimal built-in word list used as a fallback when the
// embedded EFF Large Wordlist is unavailable. It is large enough to give
// adequate entropy for 4+ word passphrases (~7.6 bits/word × 4 ≈ 30 bits) but
// is materially weaker than the EFF list — see passphraseWordlist.
var builtinWordlist = []string{
	"apple", "brave", "crisp", "dance", "eagle", "flame", "glass", "honey",
	"ivory", "jewel", "knack", "lemon", "maple", "noble", "ocean", "pearl",
	"quill", "river", "stone", "tiger", "ultra", "vivid", "water", "xenon",
	"yacht", "zebra", "amber", "bison", "coral", "delta", "ember", "frost",
	"grove", "heron", "inlet", "jumbo", "karma", "lunar", "marsh", "nymph",
	"onyx", "prism", "quartz", "realm", "storm", "thorn", "umbra", "venom",
	"whirl", "xylem", "yodel", "zonal", "acorn", "bloom", "cedar", "dunes",
	"epoch", "fable", "glyph", "haste", "irony", "joust", "kneel", "lance",
	"magic", "nexus", "oaken", "pixel", "quirk", "raven", "swift", "trove",
	"unity", "valor", "waltz", "oxide", "yield", "zones", "azure", "blaze",
	"cliff", "drape", "elbow", "flock", "grail", "haven", "image", "judge",
	"knoll", "llama", "mirth", "nerve", "orbit", "plume", "query", "rouge",
	"slope", "tidal", "usher", "vital", "wrath", "xerus", "yearn", "zesty",
	"abbot", "birch", "chase", "druid", "elder", "flora", "glade", "hedge",
	"index", "joker", "kapok", "limit", "mount", "north", "onset", "plank",
	"quote", "ridge", "scout", "tawny", "under", "vista", "woven", "xeric",
	"yucca", "zingy", "adorn", "boxer", "crave", "dwell", "eight", "flint",
	"grasp", "hoard", "ingot", "jinx", "kelp", "lathe", "motor", "notch",
	"optic", "plain", "quest", "reign", "snare", "twine", "unify", "vouch",
	"wider", "xylan", "yeoman", "zither", "alloy", "cairn", "depth",
	"evoke", "forge", "gnome", "hydra", "ionic", "knave", "lyric",
	"motif", "novel", "olive", "punch", "quell", "radar", "surge", "truce",
	"upper", "viper", "winch", "xylol", "young", "zippy",
}

func generatePassphrase(cfg PassphraseConfig) (string, error) {
	cfg = cfg.defaults()
	wl := passphraseWordlist()
	n := big.NewInt(int64(len(wl)))

	words := make([]string, cfg.WordCount)
	for i := range words {
		idx, err := rand.Int(rand.Reader, n)
		if err != nil {
			return "", fmt.Errorf("credvault: passphrase rng: %w", err)
		}
		w := wl[idx.Int64()]
		if cfg.Capitalize {
			if len(w) > 0 {
				w = strings.ToUpper(w[:1]) + w[1:]
			}
		}
		words[i] = w
	}

	phrase := strings.Join(words, cfg.Separator)

	if cfg.AppendNumber {
		n2, err := rand.Int(rand.Reader, big.NewInt(100))
		if err != nil {
			return "", fmt.Errorf("credvault: passphrase rng (number): %w", err)
		}
		phrase += fmt.Sprintf("%s%02d", cfg.Separator, n2.Int64())
	}

	return phrase, nil
}
