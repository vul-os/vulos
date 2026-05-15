package ai

import (
	"context"
	"log"
	"sync"
	"time"

	"vulos/backend/services/notify"
)

// AlertCooldown is the minimum time between repeated alerts with the same title.
// This prevents the agent from spamming notifications every check interval while
// a condition persists. Env-overridable via registering checks with WithCooldown.
const AlertCooldown = 15 * time.Minute

// ProactiveAgent monitors system state and generates AI-driven alerts.
// Instead of just "your battery is low," it says "Your battery is at 12%.
// I've switched to power saver mode and paused 2 background apps."
type ProactiveAgent struct {
	ai       *Service
	cfg      Config
	notifier *notify.Service
	checks   []ProactiveCheck

	mu        sync.Mutex
	lastAlert map[string]time.Time // title → last sent time (deduplication)
}

// ProactiveCheck is a function that examines system state and returns
// an alert if action is needed. Returns empty string if all is well.
type ProactiveCheck func(ctx context.Context) (title, body string, level notify.Level, ok bool)

func NewProactiveAgent(ai *Service, cfg Config, notifier *notify.Service) *ProactiveAgent {
	return &ProactiveAgent{
		ai:        ai,
		cfg:       cfg,
		notifier:  notifier,
		lastAlert: make(map[string]time.Time),
	}
}

// RegisterCheck adds a system check to the agent's watch list.
func (pa *ProactiveAgent) RegisterCheck(check ProactiveCheck) {
	pa.checks = append(pa.checks, check)
}

// Run executes all checks on a schedule.
func (pa *ProactiveAgent) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pa.runChecks(ctx)
		}
	}
}

// canAlert returns true if enough time has passed since the last alert with
// this title, preventing repeated notifications while a condition persists.
func (pa *ProactiveAgent) canAlert(title string) bool {
	pa.mu.Lock()
	defer pa.mu.Unlock()
	if last, ok := pa.lastAlert[title]; ok && time.Since(last) < AlertCooldown {
		return false
	}
	pa.lastAlert[title] = time.Now()
	return true
}

func (pa *ProactiveAgent) runChecks(ctx context.Context) {
	for _, check := range pa.checks {
		title, body, level, shouldAlert := check(ctx)
		if !shouldAlert {
			continue
		}
		// Deduplicate: skip if the same alert was sent recently.
		if !pa.canAlert(title) {
			continue
		}
		// Optionally enhance with AI
		if pa.cfg.APIKey != "" || pa.cfg.Provider == ProviderOllama {
			enhanced := pa.enhance(ctx, title, body)
			if enhanced != "" {
				body = enhanced
			}
		}
		pa.notifier.Send(title, body, level, "ai-agent")
		log.Printf("[proactive] alert: %s — %s", title, body)
	}
}

// enhance asks the AI to turn a raw alert into a helpful solution.
func (pa *ProactiveAgent) enhance(ctx context.Context, title, rawBody string) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	prompt := "You are a proactive OS assistant. The system detected this condition:\n\n" +
		"Title: " + title + "\nDetails: " + rawBody + "\n\n" +
		"Write a brief, helpful notification (1-2 sentences) that tells the user what happened " +
		"AND what you've already done about it or suggest an action. Be specific and concise."

	resp, err := pa.ai.Complete(ctx, pa.cfg, CompletionRequest{
		Messages:  []Message{{Role: "user", Content: prompt}},
		MaxTokens: 100,
	})
	if err != nil {
		return ""
	}
	return resp
}
