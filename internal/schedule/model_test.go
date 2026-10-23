package schedule

import (
	"reflect"
	"testing"
	"time"
)

func TestClonePreservesValuesAndOwnsMutableFields(t *testing.T) {
	at := time.Now().In(time.FixedZone("test-zone", 3600))
	original := &Task{Spec: Spec{Title: "raw\xff", Trigger: Trigger{At: &at}}, NextRunAt: &at, Runs: []Run{{EndedAt: &at}}, Active: &Run{EndedAt: &at}}
	cloned := Clone(original)
	if !reflect.DeepEqual(original, cloned) {
		t.Fatalf("clone changed values: %+v", cloned)
	}
	*cloned.Spec.Trigger.At = at.Add(time.Second)
	*cloned.NextRunAt = at.Add(time.Second)
	*cloned.Runs[0].EndedAt = at.Add(time.Second)
	*cloned.Active.EndedAt = at.Add(time.Second)
	cloned.Runs[0].Status = "changed"
	if !original.Spec.Trigger.At.Equal(at) || !original.NextRunAt.Equal(at) || !original.Runs[0].EndedAt.Equal(at) || !original.Active.EndedAt.Equal(at) || original.Runs[0].Status != "" {
		t.Fatal("clone shares mutable state")
	}
}
