// Command bot is a simple opponent: it joins a match like a game client and moves its paddle toward the ball.
// It is the second player when you run the server in local mode (see "Run the game without AWS" in the README).
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/gorilla/websocket"

	"gameserver/game"
)

func main() {
	server := flag.String("server", "ws://127.0.0.1:7777", "game server address")
	playerSession := flag.String("player-session", "bot", "player session ID from matchmaking")
	skill := flag.Float64("skill", 0.6, "how fast the bot reacts, from 0.1 to 1")
	flag.Parse()

	conn, _, err := websocket.DefaultDialer.Dial(*server, http.Header{})
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	conn.WriteJSON(map[string]string{"type": "join", "playerSessionId": *playerSession})

	var joined struct {
		Type string `json:"type"`
		Seat int    `json:"seat"`
	}
	if err := conn.ReadJSON(&joined); err != nil || joined.Type != "joined" {
		log.Fatalf("not seated: %+v %v", joined, err)
	}
	log.Printf("joined as seat %d", joined.Seat)

	for {
		var state game.Pong
		if err := conn.ReadJSON(&state); err != nil {
			log.Fatal("connection closed")
		}
		if state.Phase == game.Finished {
			log.Printf("match over, score %d-%d", state.Score[0], state.Score[1])
			return
		}
		// Move toward the ball, a little slower than a perfect player (the same computer player the server uses).
		conn.WriteJSON(map[string]any{"type": "move", "dir": game.AIMove(&state, joined.Seat, *skill)})
	}
}
