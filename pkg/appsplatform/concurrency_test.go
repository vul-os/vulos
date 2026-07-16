package appsplatform

import (
	"fmt"
	"sync"
	"testing"
)

// TestRegistryConcurrentAccess hammers the registry from many goroutines to
// surface data races (run with -race) and lock-ordering bugs. Every operation
// must remain safe and consistent under contention.
func TestRegistryConcurrentAccess(t *testing.T) {
	r := NewMemoryRegistry()

	// Seed a pool of apps to read/mutate.
	const pool = 32
	ids := make([]string, pool)
	tokens := make([]string, pool)
	for i := 0; i < pool; i++ {
		c, err := r.Create(CreateParams{Name: fmt.Sprintf("app-%d", i), OwnerID: "o", Products: []string{ProductOffice}, Scopes: []string{ScopeAppsWrite}})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = c.App.ID
		tokens[i] = c.Token
	}

	var wg sync.WaitGroup
	work := func(fn func(i int)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < pool; i++ {
				fn(i)
			}
		}()
	}

	work(func(i int) { r.Get(ids[i]) })
	work(func(i int) { r.GetByTokenHash(HashToken(tokens[i])) })
	work(func(i int) { r.List("o", false) })
	work(func(i int) { r.List("", true) })
	work(func(i int) { name := fmt.Sprintf("renamed-%d", i); r.Update(ids[i], UpdateParams{Name: &name}) })
	work(func(i int) { r.RotateToken(ids[i]) })
	work(func(i int) { r.RotateSecret(ids[i]) })
	work(func(i int) { r.AllSlashCommands(ProductOffice) })
	work(func(i int) { r.ResolveSlashCommand(ProductOffice, "deploy") })
	work(func(i int) {
		// Churn: create then delete a transient app.
		c, err := r.Create(CreateParams{Name: "tmp", OwnerID: "o", Products: []string{ProductOffice}})
		if err == nil {
			r.Delete(c.App.ID)
		}
	})

	wg.Wait()

	// The pool must still be fully present and consistent.
	for i := 0; i < pool; i++ {
		if _, err := r.Get(ids[i]); err != nil {
			t.Fatalf("app %d vanished under concurrency: %v", i, err)
		}
	}
}

// TestDispatcherConcurrentSubscribeEmit exercises Subscribe/unsubscribe racing
// against Emit, the classic place a send-on-closed-channel panic hides.
func TestDispatcherConcurrentSubscribeEmit(t *testing.T) {
	r := NewMemoryRegistry()
	c := newApp(t, r, "o", []string{ProductOffice}, nil)
	d := NewDispatcher(r, ProductOffice)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			ch, un := d.Subscribe(c.App.ID)
			_ = ch
			un()
		}()
		go func() {
			defer wg.Done()
			d.Emit(EventMessageCreated, map[string]any{"x": 1}, nil)
		}()
	}
	wg.Wait()
}
