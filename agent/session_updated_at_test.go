package agent

import (
	"testing"
	"time"
)

// TestUpdatedAt_OnlyChangesOnMutation verifies that updated_at is only
// updated when the session content actually changes (Append, SetClientCWD,
// SetSessionName), and NOT when the session is just read or fetched.
func TestUpdatedAt_OnlyChangesOnMutation(t *testing.T) {
	session := NewSession("test", "test prompt")

	// Capture the initial updated_at.
	initialUpdatedAt := session.Read().UpdatedAt

	// Sleep briefly so time advances.
	time.Sleep(time.Millisecond)

	// Reading the session should NOT change updated_at.
	_ = session.Messages()
	_ = session.SessionName()
	_ = session.SessionID()
	_ = session.Read()

	if !session.Read().UpdatedAt.Equal(initialUpdatedAt) {
		t.Fatal("reading session metadata should not change updated_at")
	}

	// Calling SetClientCWD SHOULD update updated_at.
	time.Sleep(time.Millisecond)
	session.SetClientCWD("/new/path")
	if session.Read().UpdatedAt.Equal(initialUpdatedAt) {
		t.Fatal("SetClientCWD should update updated_at")
	}
	afterCWD := session.Read().UpdatedAt

	// Calling SetSessionName SHOULD update updated_at.
	time.Sleep(time.Millisecond)
	session.SetSessionName("new-name")
	if session.Read().UpdatedAt.Equal(afterCWD) {
		t.Fatal("SetSessionName should update updated_at")
	}
	afterName := session.Read().UpdatedAt

	// Appending a message SHOULD update updated_at.
	time.Sleep(time.Millisecond)
	session.Append(Message{Role: RoleUser, Content: "hello"})
	if session.Read().UpdatedAt.Equal(afterName) {
		t.Fatal("Append should update updated_at")
	}
	afterAppend := session.Read().UpdatedAt

	// SetModel SHOULD update updated_at.
	time.Sleep(time.Millisecond)
	session.SetModel("gpt-4")
	if session.Read().UpdatedAt.Equal(afterAppend) {
		t.Fatal("SetModel should update updated_at")
	}
}

// TestStorePut_DoesNotChangeUpdatedAt verifies that saving a session to
// the store does NOT bump updated_at unless the session has been mutated.
func TestStorePut_DoesNotChangeUpdatedAt(t *testing.T) {
	store := NewMemoryStore()
	session := NewSession("test", "prompt")

	// Read initial updated_at BEFORE any Put.
	initialUpdatedAt := session.Read().UpdatedAt

	// Save the session to the store.
	if err := store.Put(nil, "test", session); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Retrieve the session and check updated_at. It should NOT have changed
	// because we didn't mutate the session after saving.
	retrieved, ok, err := store.Get(nil, "test", session.SessionID())
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}

	if !retrieved.Read().UpdatedAt.Equal(initialUpdatedAt) {
		t.Fatalf("store.Put should not change updated_at; got %v, want %v",
			retrieved.Read().UpdatedAt, initialUpdatedAt)
	}
}
