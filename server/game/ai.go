package game

// AIMove is the computer player: it moves its paddle toward the ball, a little slower than a perfect player,
// so a person can beat it. It returns the same input a client sends: -1 (up) to 1 (down), 0 to stay.
// skill sets how fast it reacts, from 0.1 to 1.
//
// Two programs use it:
//   - The game server, to play the empty seat when FlexMatch started the match with one player (Config.Humans
//     = 1). The computer runs inside the match loop, so it has no connection, no player session, and no
//     network delay.
//   - cmd/bot, a separate program that joins like a game client. It is the second player when you run the
//     server in local mode, without Amazon GameLift Servers.
//
// Because the server decides every move, a computer player is not a special case for the rules: it sends
// the same kind of input as a person, and Pong.Step treats both the same.
func AIMove(p *Pong, seat int, skill float64) float64 {
	gap := p.BallY - p.Paddles[seat]
	return max(-1, min(1, gap/40)) * max(0.1, min(1, skill))
}
