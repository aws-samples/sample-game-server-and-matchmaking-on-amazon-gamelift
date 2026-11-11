package game

import (
	"math"
	"testing"
)

const dt = 1.0 / 60

func started(t *testing.T, scoreToWin int) *Pong {
	t.Helper()
	p := NewPong(scoreToWin)
	p.Start()
	for i := 0; i < int(ServeDelay/dt)+2; i++ {
		p.Step(dt, [2]float64{})
	}
	if p.Phase != Playing {
		t.Fatalf("phase = %s, want playing", p.Phase)
	}
	return p
}

func TestWaitingDoesNothing(t *testing.T) {
	p := NewPong(3)
	p.Step(1, [2]float64{1, 1})
	if p.Paddles != [2]float64{Height / 2, Height / 2} {
		t.Fatalf("paddles moved before the match started: %v", p.Paddles)
	}
}

func TestFirstServeGoesLeft(t *testing.T) {
	if p := started(t, 3); p.velX >= 0 {
		t.Fatalf("first serve should travel toward seat 0, velX = %v", p.velX)
	}
}

func TestPaddlesStayOnFieldAndIgnoreBadInput(t *testing.T) {
	p := started(t, 3)
	for i := 0; i < 200; i++ {
		p.Step(dt, [2]float64{-50, math.NaN()})
	}
	if p.Paddles[0] != PaddleHeight/2 {
		t.Fatalf("seat 0 paddle = %v, want clamped to %v", p.Paddles[0], PaddleHeight/2)
	}
	if p.Paddles[1] != Height/2 {
		t.Fatalf("NaN input moved seat 1: %v", p.Paddles[1])
	}
}

func TestBallBouncesOffWall(t *testing.T) {
	p := started(t, 3)
	p.BallX, p.BallY, p.velX, p.velY = Width/2, BallRadius+1, 100, -300
	p.Step(dt, [2]float64{})
	if p.velY <= 0 {
		t.Fatalf("ball did not bounce off the top wall: velY = %v", p.velY)
	}
}

func TestPaddleReturnsBallFaster(t *testing.T) {
	p := started(t, 3)
	p.BallX, p.BallY, p.velX, p.velY = PaddleInset+BallRadius+2, p.Paddles[0], -400, 0
	p.Step(dt, [2]float64{})
	if p.velX <= 0 {
		t.Fatalf("paddle did not return the ball: velX = %v", p.velX)
	}
	if speed := math.Hypot(p.velX, p.velY); speed <= 400 {
		t.Fatalf("ball should speed up on a hit, speed = %v", speed)
	}
}

func TestMissIsAGoalAndLastGoalWins(t *testing.T) {
	p := started(t, 2)
	for goal := 1; goal <= 2; goal++ {
		for i := 0; i < int(ServeDelay/dt)+2 && p.Phase == Serving; i++ {
			p.Step(dt, [2]float64{})
		}
		p.Paddles[0] = PaddleHeight / 2 // seat 0 stands at the top and misses
		p.BallX, p.BallY, p.velX, p.velY = PaddleInset+20, Height-20, -600, 0
		for i := 0; i < 30 && p.Score[1] < goal; i++ {
			p.Step(dt, [2]float64{-1, 0})
		}
		if p.Score[1] != goal {
			t.Fatalf("goal %d not counted, score %v", goal, p.Score)
		}
	}
	if p.Phase != Finished || p.Winner != 1 {
		t.Fatalf("phase %s winner %d, want finished, seat 1", p.Phase, p.Winner)
	}
}

func TestForfeit(t *testing.T) {
	p := started(t, 3)
	p.Forfeit(1)
	p.Forfeit(0) // a finished match keeps its winner
	if p.Phase != Finished || p.Winner != 0 {
		t.Fatalf("phase %s winner %d, want finished, seat 0", p.Phase, p.Winner)
	}
}
