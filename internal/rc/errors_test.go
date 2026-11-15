package rc

import "testing"

// TestIsMethodMissingDistinguishesObjectNotFound pins that a 404 for a missing
// object is not mistaken for an rclone build without the method: callers such
// as the split-manifest reader treat an object 404 as "the file is absent".
func TestIsMethodMissingDistinguishesObjectNotFound(t *testing.T) {
	object := &Error{Status: 404, Path: "operations/cat", Message: "object not found"}
	if !IsNotFound(object) {
		t.Fatalf("IsNotFound(%v) = false, want true", object)
	}
	if IsMethodMissing(object) {
		t.Fatalf("IsMethodMissing(%v) = true, want false", object)
	}

	for _, msg := range []string{
		`couldn't find method "operations/cat"`,
		`Didn't find method "operations/cat"`,
	} {
		err := &Error{Status: 404, Path: "operations/cat", Message: msg}
		if !IsMethodMissing(err) {
			t.Fatalf("IsMethodMissing(%q) = false, want true", msg)
		}
	}
}
