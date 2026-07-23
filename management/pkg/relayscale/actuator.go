package relayscale

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
)

// actuator.go — reconciles the policy's DESIRED STATE through the injected
// RelayProvisioner. This is the one place that turns a Plan into Provision /
// Destroy calls. It holds no provider knowledge: it only diffs desired-vs-actual
// and drives the seam.

// SpecFunc yields the RelaySpec for a relay to be provisioned in region. The
// composition root supplies it (the base domain, image, soft caps, CP wiring).
// A nil SpecFunc yields a zero spec (the provisioner then applies its defaults).
type SpecFunc func(region string) RelaySpec

// Actuator reconciles a set of region loads against a RelayProvisioner using a
// Policy. Construct with NewActuator. Reconcile is safe to call serially from a
// single loop; do not call it concurrently with itself.
type Actuator struct {
	prov   RelayProvisioner
	policy Policy
	spec   SpecFunc
	log    *slog.Logger
}

// NewActuator builds an Actuator. A nil provisioner defaults to ManualProvisioner
// (provisions nothing); a nil logger uses slog.Default().
func NewActuator(prov RelayProvisioner, policy Policy, spec SpecFunc, log *slog.Logger) *Actuator {
	if prov == nil {
		prov = NewManualProvisioner()
	}
	if log == nil {
		log = slog.Default()
	}
	return &Actuator{prov: prov, policy: policy, spec: spec, log: log}
}

// Provisioner returns the underlying seam impl (for the demand API to name it).
func (a *Actuator) Provisioner() RelayProvisioner { return a.prov }

// Policy returns the effective policy (for the demand API to compute desired
// counts consistently with the actuator).
func (a *Actuator) Policy() Policy { return a.policy }

// Result summarises one Reconcile pass.
type Result struct {
	// Plans is the policy's desired-state decision per region.
	Plans []RegionPlan
	// Provisioned / Destroyed are the instances actually actuated this pass.
	Provisioned []Instance
	Destroyed   []Instance
	// Actuated reports whether the provisioner performed real changes. False in
	// manual/external mode — the Plans are still returned so a caller (the demand
	// API) can surface them to an external scaler.
	Actuated bool
	// Errs collects per-action errors that did not abort the whole pass.
	Errs []error
}

// Reconcile computes the plan from loads and drives the provisioner to converge.
// In manual/external mode it performs no changes and returns Actuated=false with
// the plan populated. Provider errors on individual actions are collected in
// Result.Errs rather than aborting the whole pass, so one wedged region does not
// stall the others.
func (a *Actuator) Reconcile(ctx context.Context, loads []RegionLoad) (Result, error) {
	res := Result{Plans: a.policy.Plan(loads)}

	if !a.prov.Enabled() {
		// Manual / external mode: nothing to actuate. The desired state is still
		// computed and returned so the demand API can publish it.
		a.log.Debug("relayscale: provisioner does not actuate; publishing demand only",
			"provisioner", a.prov.Name(), "regions", len(res.Plans))
		return res, nil
	}
	res.Actuated = true

	// Snapshot what the provisioner currently manages, bucketed by region.
	existing, err := a.prov.List(ctx)
	if err != nil {
		return res, fmt.Errorf("relayscale: list instances: %w", err)
	}
	byRegion := map[string][]Instance{}
	for _, inst := range existing {
		byRegion[inst.Region] = append(byRegion[inst.Region], inst)
	}

	for _, plan := range res.Plans {
		delta := plan.Delta()
		switch {
		case delta > 0:
			for i := 0; i < delta; i++ {
				if err := ctx.Err(); err != nil {
					return res, err
				}
				spec := RelaySpec{}
				if a.spec != nil {
					spec = a.spec(plan.Region)
				}
				inst, err := a.prov.Provision(ctx, plan.Region, spec)
				if err != nil {
					// A manual/external provisioner that slips through Enabled()==true
					// still signals its mode cleanly; record and move on.
					if errors.Is(err, ErrManualMode) || errors.Is(err, ErrExternalMode) {
						res.Actuated = false
						a.log.Info("relayscale: provisioner declined to actuate", "provisioner", a.prov.Name(), "region", plan.Region)
						break
					}
					res.Errs = append(res.Errs, fmt.Errorf("provision %s: %w", plan.Region, err))
					continue
				}
				res.Provisioned = append(res.Provisioned, inst)
				a.log.Info("relayscale: provisioned relay", "provisioner", a.prov.Name(), "region", plan.Region, "id", inst.ID)
			}
		case delta < 0:
			victims := pickVictims(byRegion[plan.Region], -delta)
			for _, v := range victims {
				if err := ctx.Err(); err != nil {
					return res, err
				}
				if err := a.prov.Destroy(ctx, v); err != nil {
					res.Errs = append(res.Errs, fmt.Errorf("destroy %s/%s: %w", plan.Region, v.ID, err))
					continue
				}
				res.Destroyed = append(res.Destroyed, v)
				a.log.Info("relayscale: destroyed relay", "provisioner", a.prov.Name(), "region", plan.Region, "id", v.ID)
			}
		}
	}
	return res, nil
}

// pickVictims chooses n instances to destroy from a region: prefer not-ready
// (still-coming-up or wedged) nodes first, then the newest ready nodes (youngest
// carry the least established traffic), by id for determinism. It never returns
// more than len(insts).
func pickVictims(insts []Instance, n int) []Instance {
	if n >= len(insts) {
		// Destroying everything in the region would violate the floor; the policy
		// already clamps to MinPerRegion, so this only happens when List and the
		// observed count disagree. Be conservative: keep one.
		if len(insts) <= 1 {
			return nil
		}
		n = len(insts) - 1
	}
	cp := append([]Instance(nil), insts...)
	sort.Slice(cp, func(i, j int) bool {
		if cp[i].Ready != cp[j].Ready {
			return !cp[i].Ready // not-ready first
		}
		if !cp[i].CreatedAt.Equal(cp[j].CreatedAt) {
			return cp[i].CreatedAt.After(cp[j].CreatedAt) // newest first
		}
		return cp[i].ID < cp[j].ID
	})
	return cp[:n]
}
