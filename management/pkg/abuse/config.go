package abuse

import "time"

// Config tunes the detector + the suspend policy. All fields are optional;
// zero values are replaced with sensible defaults by withDefaults.
type Config struct {
	// Window is the sliding-window length the per-account / per-IP counters
	// look back over. Default: 1 minute.
	Window time.Duration

	// SignupBurstPerIP is the count-per-Window at which a per-IP signup burst
	// becomes a Strong signal (score=1.0). Default: 20.
	SignupBurstPerIP int

	// SendBurstPerAccount is the count-per-Window at which a per-account send
	// burst becomes a Strong signal. Default: 200.
	SendBurstPerAccount int

	// SendBurstPerIP is the count-per-Window at which a per-IP send burst
	// becomes a Strong signal. Default: 500.
	SendBurstPerIP int

	// LoginBurstPerIP is the count-per-Window at which a per-IP login burst
	// (success OR failure — credential stuffing tell) becomes a Strong signal.
	// Default: 50.
	LoginBurstPerIP int

	// FanoutRecipientsStrong is the single-message recipient count at which a
	// fan-out becomes a Strong signal. Default: 200.
	FanoutRecipientsStrong int

	// FanoutRecipientsWarn is the single-message recipient count at which a
	// fan-out becomes a Warn signal. Default: 50.
	FanoutRecipientsWarn int

	// WarnThreshold and StrongThreshold gate Signal.IsWarn / IsStrong. The
	// detector emits scores normalised so 1.0 = StrongThreshold met.
	// Defaults: 0.5 / 1.0.
	WarnThreshold   float64
	StrongThreshold float64
}

func (c Config) withDefaults() Config {
	if c.Window <= 0 {
		c.Window = time.Minute
	}
	if c.SignupBurstPerIP <= 0 {
		c.SignupBurstPerIP = 20
	}
	if c.SendBurstPerAccount <= 0 {
		c.SendBurstPerAccount = 200
	}
	if c.SendBurstPerIP <= 0 {
		c.SendBurstPerIP = 500
	}
	if c.LoginBurstPerIP <= 0 {
		c.LoginBurstPerIP = 50
	}
	if c.FanoutRecipientsStrong <= 0 {
		c.FanoutRecipientsStrong = 200
	}
	if c.FanoutRecipientsWarn <= 0 {
		c.FanoutRecipientsWarn = 50
	}
	if c.WarnThreshold <= 0 {
		c.WarnThreshold = 0.5
	}
	if c.StrongThreshold <= 0 {
		c.StrongThreshold = 1.0
	}
	return c
}
