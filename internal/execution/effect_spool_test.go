package execution

import (
	"context"
	"reflect"
	"testing"
)

func TestEffectEnvironmentIsTaskLocal(t *testing.T) {
	original := []string{"PATH=/bin", "CLAWFLOW_EFFECT_SPOOL=old"}
	a := WithEffectSpool(context.Background(), "/tmp/a")
	b := WithEffectSpool(context.Background(), "/tmp/b")
	if got := effectEnvironment(a, original); !reflect.DeepEqual(got, []string{"PATH=/bin", "CLAWFLOW_EFFECT_SPOOL=/tmp/a"}) {
		t.Fatal(got)
	}
	if got := effectEnvironment(b, original); got[1] != "CLAWFLOW_EFFECT_SPOOL=/tmp/b" {
		t.Fatal(got)
	}
	if got := effectEnvironment(context.Background(), original); !reflect.DeepEqual(got, original) || original[1] != "CLAWFLOW_EFFECT_SPOOL=old" {
		t.Fatal("environment mutated")
	}
}
