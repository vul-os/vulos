package appsplatform

import (
	"context"
	"encoding/json"
)

// ProductAdapter is THE PRODUCT SEAM: how the platform posts/acts into a
// specific product's own surface. Each product implements this once for its
// surface and supplies it when mounting the handler set:
//
//	Office -> invoke an office tool / read a document range
//	Board  -> apply a board op / read a scene region
//	Files  -> perform a files op (put, move, share) / read a listing
//	OS     -> perform an OS file op / read a directory
//
// The platform owns authentication, token hashing, product-targeting and scope
// enforcement; the adapter owns the product-native semantics. The platform asks
// the adapter which scope an action/read kind requires (RequiredScope) and
// whether the app may touch a target (CanAccessTarget) before invoking Act/Read.
type ProductAdapter interface {
	// Product returns which product this adapter serves (one of the Product*
	// constants). The handler only lists / serves apps that target it.
	Product() string

	// RequiredScope returns the scope an action (for Act) or kind (for Read)
	// requires. Return "" to require no scope. If non-empty and the app lacks
	// the scope, the platform responds 403 before calling Act/Read.
	RequiredScope(actionOrKind string) string

	// CanAccessTarget reports whether app may act on / read target within this
	// product (channel / folder / room / doc visibility). exists=false yields a
	// 404; allowed=false a 403. An empty target is treated as accessible (the
	// action itself is target-less).
	CanAccessTarget(app *App, target string) (allowed, exists bool)

	// Act performs the product-native action an app requests at runtime
	// (POST {base}/v1/act). It returns a JSON-serializable result (echoed to the
	// caller) or an error. emit lets the action fan out a follow-up platform
	// event (e.g. message.created) so other apps see it; it may be nil.
	Act(ctx context.Context, app *App, req ActionRequest, emit EmitFunc) (any, error)

	// Read returns product content for a target (GET {base}/v1/read), e.g. an
	// office doc range / board scene region / files listing / OS directory.
	Read(ctx context.Context, app *App, req ReadRequest) (any, error)
}

// ActionRequest is the generic runtime action envelope an app POSTs to
// {base}/v1/act. Payload is product-specific and left opaque to the platform.
type ActionRequest struct {
	Action  string          `json:"action"`  // product-defined, e.g. "doc.edit", "file.put"
	Target  string          `json:"target"`  // channel/folder/room/doc id (optional)
	Payload json.RawMessage `json:"payload"` // product-specific body
}

// ReadRequest is the generic runtime read envelope for GET {base}/v1/read.
// Kind, Target and the remaining query params are passed through to the adapter.
type ReadRequest struct {
	Kind   string            `json:"kind"`   // product-defined, e.g. "history", "members"
	Target string            `json:"target"` // channel/folder/room/doc id (optional)
	Params map[string]string `json:"params"` // remaining query parameters
}

// EmitFunc fans a follow-up event out to subscribed apps. It is handed to
// ProductAdapter.Act so a product action (e.g. an app posting a message) can
// notify other apps. recipients filters which apps receive it (visibility); a
// nil recipients delivers to every subscribed app that targets the product.
type EmitFunc func(eventType string, payload map[string]any, recipients func(*App) bool)
