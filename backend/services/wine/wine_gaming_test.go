package wine

import "testing"

// TestIsGamingCommand verifies GAME-07 helper detection logic.
func TestIsGamingCommand(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		// Gaming launchers — should return true
		{"wine", true},
		{"wine64", true},
		{"lutris", true},
		{"steam", true},
		{"steam-runtime", true},
		// Absolute paths — basename detected
		{"/usr/bin/wine", true},
		{"/usr/bin/lutris", true},
		// With arguments — first token only
		{"wine game.exe /fullscreen", true},
		{"steam -applaunch 42", true},
		// Non-gaming commands — should return false
		{"kicad", false},
		{"firefox", false},
		{"python3 server.py", false},
		{"", false},
	}

	for _, tc := range cases {
		got := IsGamingCommand(tc.cmd)
		if got != tc.want {
			t.Errorf("IsGamingCommand(%q) = %v, want %v", tc.cmd, got, tc.want)
		}
	}
}
