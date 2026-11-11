// Package game is the demo game: two-player Pong, run by the server. It contains no Amazon GameLift Servers
// code; if you are here to learn Amazon GameLift Servers, read package gamelift instead.
//
// pong.go holds the rules. They have no networking and no clock: the match loop in match.go calls Step 60
// times a second, which keeps the rules simple to read and to test.
package game

import "math"

// Field and speeds, in pixels and seconds. Seat 0 plays on the left, seat 1 on the right.
const (
	Width        = 800.0
	Height       = 500.0
	PaddleHeight = 100.0
	PaddleInset  = 30.0 // distance from each side wall to the face of its paddle
	BallRadius   = 8.0
	PaddleSpeed  = 450.0 // pixels per second at full input
	ServeSpeed   = 350.0
	MaxBallSpeed = 900.0
	SpeedUp      = 1.06 // the ball gets this much faster on every paddle hit
	ServeDelay   = 1.0  // seconds between a goal and the next serve
)

// Phase is the stage of a match.
type Phase string

const (
	Waiting  Phase = "waiting" // until both players join
	Serving  Phase = "serving" // a short pause before the ball moves
	Playing  Phase = "playing"
	Finished Phase = "finished"
)

// Pong is the full state of one match. Only the server changes it.
type Pong struct {
	Phase      Phase      `json:"phase"`
	BallX      float64    `json:"ballX"`
	BallY      float64    `json:"ballY"`
	Paddles    [2]float64 `json:"paddles"` // the vertical center of each paddle
	Score      [2]int     `json:"score"`
	Winner     int        `json:"winner"` // -1 until the match finishes
	ScoreToWin int        `json:"-"`

	velX, velY float64
	serveTimer float64
	serveTo    int // the seat the next serve travels toward
}

// NewPong returns a match that waits for both players.
func NewPong(scoreToWin int) *Pong {
	p := &Pong{Phase: Waiting, Winner: -1, ScoreToWin: max(scoreToWin, 1)}
	p.Paddles = [2]float64{Height / 2, Height / 2}
	p.BallX, p.BallY = Width/2, Height/2
	return p
}

// Start begins the first serve.
func (p *Pong) Start() {
	if p.Phase == Waiting {
		p.serve(0)
	}
}

// Forfeit ends the match in favor of the other seat, for example when a player leaves.
func (p *Pong) Forfeit(seat int) {
	if p.Phase != Finished {
		p.Phase, p.Winner = Finished, 1-seat
	}
}

// Step advances the match by dt seconds. move is each player's input: -1 up, 0 stay, 1 down.
func (p *Pong) Step(dt float64, move [2]float64) {
	if p.Phase != Serving && p.Phase != Playing {
		return
	}
	for seat := range p.Paddles {
		m := move[seat]
		if math.IsNaN(m) {
			m = 0
		}
		m = math.Max(-1, math.Min(1, m)) // never trust the client's number
		p.Paddles[seat] = clamp(p.Paddles[seat]+m*PaddleSpeed*dt, PaddleHeight/2, Height-PaddleHeight/2)
	}

	if p.Phase == Serving {
		if p.serveTimer -= dt; p.serveTimer <= 0 {
			p.Phase = Playing
			p.velX = ServeSpeed
			if p.serveTo == 0 {
				p.velX = -ServeSpeed
			}
			p.velY = ServeSpeed * 0.3
		}
		return
	}

	p.BallX += p.velX * dt
	p.BallY += p.velY * dt

	// Top and bottom walls.
	if p.BallY < BallRadius {
		p.BallY, p.velY = BallRadius, math.Abs(p.velY)
	} else if p.BallY > Height-BallRadius {
		p.BallY, p.velY = Height-BallRadius, -math.Abs(p.velY)
	}

	// Paddles. Where the ball hits the paddle sets its new angle, so players can aim.
	if p.velX < 0 && p.BallX-BallRadius <= PaddleInset && p.hits(0) {
		p.bounce(0, PaddleInset+BallRadius)
	} else if p.velX > 0 && p.BallX+BallRadius >= Width-PaddleInset && p.hits(1) {
		p.bounce(1, Width-PaddleInset-BallRadius)
	}

	// Goals: the ball left the field past a paddle.
	if p.BallX < 0 {
		p.goal(1)
	} else if p.BallX > Width {
		p.goal(0)
	}
}

func (p *Pong) hits(seat int) bool {
	return math.Abs(p.BallY-p.Paddles[seat]) <= PaddleHeight/2+BallRadius
}

func (p *Pong) bounce(seat int, x float64) {
	speed := math.Min(math.Hypot(p.velX, p.velY)*SpeedUp, MaxBallSpeed)
	offset := (p.BallY - p.Paddles[seat]) / (PaddleHeight/2 + BallRadius) // -1 top edge .. 1 bottom edge
	angle := offset * math.Pi / 3                                         // up to 60 degrees
	dir := 1.0
	if seat == 1 {
		dir = -1
	}
	p.BallX = x
	p.velX, p.velY = dir*speed*math.Cos(angle), speed*math.Sin(angle)
}

func (p *Pong) goal(scorer int) {
	p.Score[scorer]++
	if p.Score[scorer] >= p.ScoreToWin {
		p.Phase, p.Winner = Finished, scorer
		return
	}
	p.serve(1 - scorer) // the player who conceded receives the serve
}

func (p *Pong) serve(toward int) {
	p.Phase, p.serveTimer, p.serveTo = Serving, ServeDelay, toward
	p.BallX, p.BallY = Width/2, Height/2
	p.velX, p.velY = 0, 0
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }
