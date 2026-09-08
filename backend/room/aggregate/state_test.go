package aggregate

import "testing"

func TestCanRoomTransition(t *testing.T) {
	cases := []struct {
		from, to string
		want     bool
	}{
		{RoomStatusScheduled, RoomStatusActive, true},
		{RoomStatusScheduled, RoomStatusArchived, true},
		{RoomStatusActive, RoomStatusClosed, true},
		{RoomStatusActive, RoomStatusArchived, true},
		{RoomStatusClosed, RoomStatusActive, true}, // 复开
		{RoomStatusClosed, RoomStatusArchived, true},
		{RoomStatusArchived, RoomStatusActive, false},
		{RoomStatusArchived, RoomStatusClosed, false},
		{RoomStatusScheduled, RoomStatusClosed, false},
		{"bogus", RoomStatusActive, false},
	}
	for _, c := range cases {
		if got := CanRoomTransition(c.from, c.to); got != c.want {
			t.Errorf("CanRoomTransition(%q, %q)=%v, want %v", c.from, c.to, got, c.want)
		}
	}
}
