package collections

import (
	"reflect"
	"testing"
)

func TestDedupUnionSortedKeys(t *testing.T) {
	if got := Dedup([]string{"a", "b", "a"}); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("dedup = %v", got)
	}
	if got := Union([]string{"a", "b"}, []string{"b", "c"}); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("union = %v", got)
	}
	if got := SortedKeys(map[string]int{"b": 2, "a": 1}); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("keys = %v", got)
	}
}
