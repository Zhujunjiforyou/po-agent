package provider

import (
	"errors"
	"testing"
)

func TestErrorFormatsAndUnwrapsCause(t *testing.T) {
	cause := errors.New("connection reset")
	tests := []struct {
		name string
		err  *Error
		want string
	}{
		{
			name: "transport",
			err:  &Error{Op: "chat.completions", Err: cause},
			want: "provider chat.completions failed: connection reset",
		},
		{
			name: "http",
			err:  &Error{Op: "chat.completions", StatusCode: 429, Err: cause},
			want: "provider chat.completions failed with HTTP 429: connection reset",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.err.Error(); got != test.want {
				t.Fatalf("Error() = %q, want %q", got, test.want)
			}
			if !errors.Is(test.err, cause) {
				t.Fatal("provider error does not unwrap its cause")
			}
		})
	}
}

func TestNilErrorMethods(t *testing.T) {
	var err *Error
	if err.Error() != "<nil>" || err.Unwrap() != nil {
		t.Fatalf("nil error methods returned Error=%q Unwrap=%v", err.Error(), err.Unwrap())
	}
}
