package rules

import (
	"reflect"
	"testing"

	"github.com/cernly/cernly/fact"
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

// Two stacks of the same name in different kinds keep their own role: a
// datastore and a route of the helm stack named prod are not production when
// only the kubernetes stack named prod is.
func TestProductionByStackKey(t *testing.T) {
	m := &Map{
		Stacks: []StackRole{{Stack: "stack:kubernetes:prod", Role: RoleProduction}, {Stack: "stack:helm:prod", Role: RoleNone}},
		Routes: []RouteEdge{{Route: "route:k", Stack: "stack:kubernetes:prod"}, {Route: "route:h", Stack: "stack:helm:prod"}},
	}
	facts := []fact.Fact{
		{Kind: fact.KindDatastore, Key: "datastore:helm:prod:db", Attrs: map[string]any{"stack": "prod"}},
		{Kind: fact.KindDatastore, Key: "datastore:kubernetes:prod:db", Attrs: map[string]any{"stack": "prod"}},
	}
	mx := metrics(m, facts)
	if !reflect.DeepEqual(mx.Production.Items.Routes, []string{"route:k"}) {
		t.Errorf("production routes %v", mx.Production.Items.Routes)
	}
	if !reflect.DeepEqual(mx.Production.Items.DataStores, []string{"datastore:kubernetes:prod:db"}) {
		t.Errorf("production data stores %v", mx.Production.Items.DataStores)
	}
}
