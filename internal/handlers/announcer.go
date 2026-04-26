package handlers

// Announcer posts a message to the configured Discord announcement channel.
// It is a no-op when no channel is configured.
type Announcer interface {
	Announce(msg string)
}
