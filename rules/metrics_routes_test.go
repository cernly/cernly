package rules

import (
	"reflect"
	"testing"
)

// A route's stack is a full key (stack:compose:p); the production route
// count and items must still find its role.
func TestProductionRoutesCounted(t *testing.T) {
	m := &Map{
		Stacks: []StackRole{{Stack: "stack:compose:p", Role: RoleProduction}, {Stack: "stack:compose:dev", Role: RoleNone}},
		Routes: []RouteEdge{{Route: "route:a", Stack: "stack:compose:p"}, {Route: "route:b", Stack: "stack:compose:dev"}},
	}
	mx := metrics(m, nil)
	if mx.Production.Routes != 1 || !reflect.DeepEqual(mx.Production.Items.Routes, []string{"route:a"}) {
		t.Errorf("production routes %d %v", mx.Production.Routes, mx.Production.Items.Routes)
	}
	if mx.Total.Routes != 2 {
		t.Errorf("total routes %d", mx.Total.Routes)
	}
}
