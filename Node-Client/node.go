package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"sync"
	"time"
)

type Pos struct {
	X int
	Y int
}

// Peers
type Node struct {
	Id        string
	Ip        string // udp port this node is listening to
	CurrLoc   *Pos
	Direction string
}

// Message to be passed among nodes.
type Message struct {
	IsLeader          bool              // is this from the leader.
	IsDirectionChange bool              // is this a direction change update.
	IsDeathReport     bool              // is this a death report.
	DeadNodes         []string          // id of dead nodes.
	Node              Node              // interval update struct.
	History           map[string][]*Pos // Id to a list of locations
}

const (
	BOARD_SIZE       int    = 10
	CHECKIN_INTERVAL int    = 200
	DIRECTION_UP     string = "U"
	DIRECTION_DOWN   string = "D"
	DIRECTION_LEFT   string = "L"
	DIRECTION_RIGHT  string = "R"
)

// Game variables.
var isPlaying bool        // Is the game in session.
var imAlive bool          // Am I alive.
var nodeId string         // Name of client.
var nodeIndex string      // Player number (1 - 6).
var nodeAddr string       // IP of client.
var httpServerAddr string // HTTP Server IP.
var nodes []*Node         // All nodes in the game.
var myNode *Node          // My node.
var PeerHistory map[string][]*Pos
var aliveNodes int // Number of alive nodes.

// #LEADER specific.
var deadNodes []string // id of dead nodes found.

// Sync variables.
var waitGroup sync.WaitGroup // For internal processes.

// Game timers in milliseconds.
var intervalUpdateRate time.Duration
var tickRate time.Duration

var board [BOARD_SIZE][BOARD_SIZE]string
var directions map[string]string
var initialPosition map[string]*Pos
var lastCheckin map[string]time.Time

func main() {
	if len(os.Args) != 5 {
		log.Println("usage: NodeClient [nodeAddr] [nodeRpcAddr] [msServerAddr] [httpServerAddr]")
		log.Println("[nodeAddr] the udp ip:port node is listening to")
		log.Println("[nodeRpcAddr] the rpc ip:port node is hosting for ms server")
		log.Println("[msServerAddr] the rpc ip:port of matchmaking server node is connecting to")
		log.Println("[httpServerAddr] the ip:port the http server is binded to ")
		os.Exit(1)
	}

	nodeAddr, nodeRpcAddr, msServerAddr = os.Args[1], os.Args[2], os.Args[3]

	httpServerTcpAddr, err := net.ResolveTCPAddr("tcp", os.Args[4])
	checkErr(err)
	httpServerAddr = httpServerTcpAddr.String()

	log.Println(nodeAddr, nodeRpcAddr, msServerAddr, httpServerAddr)
	initLogging()

	waitGroup.Add(2) // Add internal process.
	go msRpcServce()
	go httpServe()
	waitGroup.Wait() // Wait until processes are done.
}

// Initialize variables.
func init() {
	directions = map[string]string{
		"p1": DIRECTION_RIGHT,
		"p2": DIRECTION_LEFT,
		"p3": DIRECTION_LEFT,
		"p4": DIRECTION_RIGHT,
		"p5": DIRECTION_RIGHT,
		"p6": DIRECTION_LEFT,
	}

	initialPosition = map[string]*Pos{
		"p1": &Pos{1, 1},
		"p2": &Pos{8, 8},
		"p3": &Pos{8, 1},
		"p4": &Pos{1, 8},
		"p5": &Pos{4, 1},
		"p6": &Pos{5, 8},
	}

	for player, pos := range initialPosition {
		board[pos.X][pos.Y] = player
	}

	nodes = make([]*Node, 0)
	PeerHistory = make(map[string][]*Pos)
	lastCheckin = make(map[string]time.Time)
	deadNodes = make([]string, 0)
	tickRate = 500 * time.Millisecond
	intervalUpdateRate = 500 * time.Millisecond // TODO we said it's 100 in proposal?
}

func intMax(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func intMin(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func startGame() {
	// Find myself and init variables.
	for i, node := range nodes {
		node.CurrLoc = initialPosition[node.Id]
		node.Direction = directions[node.Id]
		if node.Ip == nodeAddr {
			myNode = node
			nodeId = node.Id
			nodeIndex = strconv.Itoa(i + 1)
		}
		lastCheckin[node.Id] = time.Now()
	}

	// ================================================= //

	imAlive = true
	isPlaying = true
	aliveNodes = len(nodes)

	go listenUDPPacket()
	go intervalUpdate()
	go tickGame()
	go handleNodeFailure()
}

// Update the board based on leader's history
func UpdateBoard() {
	fmt.Println("Updating Board")
	for id, _ := range PeerHistory {
		buf := []byte(id)
		playerIndex := string(buf[1])
		for i, pos := range PeerHistory[id] {
			if i == len(PeerHistory[id])-1 {
				board[pos.Y][pos.X] = "p" + playerIndex
			} else {
				board[pos.Y][pos.X] = "t" + playerIndex
			}
		}
	}
}

// Each tick of the game
func tickGame() {
	if isPlaying == false {
		return
	}

	for {
		if isPlaying == false {
			return
		}
		for i, node := range nodes {
			playerIndex := i + 1
			direction := node.Direction
			x := node.CurrLoc.X
			y := node.CurrLoc.Y
			new_x := node.CurrLoc.X
			new_y := node.CurrLoc.Y

			// Path prediction
			board[y][x] = "t" + strconv.Itoa(playerIndex) // Change position to be a trail.
			switch direction {
			case DIRECTION_UP:
				new_y = intMax(0, y-1)
			case DIRECTION_DOWN:
				new_y = intMin(BOARD_SIZE-1, y+1)
			case DIRECTION_LEFT:
				new_x = intMax(0, x-1)
			case DIRECTION_RIGHT:
				new_x = intMin(BOARD_SIZE-1, x+1)
			}

			if nodeHasCollided(x, y, new_x, new_y) {
				localLog("NODE " + node.Id + " IS DEAD")
				// We don't update the position to a new value
				board[y][x] = "d" + strconv.Itoa(playerIndex) // Dead node
				if node.Id == nodeId && imAlive {
					imAlive = false
					if gSO != nil {
						gSO.Emit("playerDead")
						reportMySorrowfulDeath()
					} else {
						log.Fatal("Socket object somehow still not set up")
					}
				}
			} else {
				// Update player's new position.
				board[new_y][new_x] = "p" + strconv.Itoa(playerIndex)
				node.CurrLoc.X = new_x
				node.CurrLoc.Y = new_y
			}
		}

		renderGame()
		time.Sleep(tickRate)
	}
}

// Change Position of a node by creating a trail from its previous location.
// (Predicting a path from a given prev location and new location).
func updateLocationOfNode(fromCurrent *Node, to *Node) {
	currentDir := fromCurrent.Direction
	currentX := fromCurrent.CurrLoc.X
	currentY := fromCurrent.CurrLoc.Y

	newDir := to.Direction
	newX := to.CurrLoc.X
	newY := to.CurrLoc.Y

	empty := ""
	nodeName := to.Id // p1, p2, etc.
	nodeTrail := "t" + nodeName[len(nodeName)-1:]

	if currentX == newX && currentY == newY {
		fromCurrent.Direction = newDir
	} else {
		if currentDir == DIRECTION_UP {
			board[currentY][currentX] = empty
			i := currentY
			for i > newY {
				board[i][currentX] = nodeTrail
				i--
			}
			board[newY][currentX] = nodeName
			fromCurrent.CurrLoc.Y = newY
		} else if currentDir == DIRECTION_DOWN {
			board[currentY][currentX] = empty
			i := currentY
			for i < newY {
				board[i][currentX] = nodeTrail
				i++
			}
			board[newY][currentX] = nodeName
			fromCurrent.CurrLoc.Y = newY
		} else if currentDir == DIRECTION_LEFT {
			board[currentY][currentX] = empty
			i := currentX
			for i > newX {
				board[currentY][i] = nodeTrail
				i--
			}
			board[currentY][newX] = nodeName
			fromCurrent.CurrLoc.X = newX
		} else { // DIRECTION_RIGHT
			board[currentY][currentX] = empty
			i := currentX
			for i < newX {
				board[currentY][i] = nodeTrail
				i++
			}
			board[currentY][newX] = nodeName
			fromCurrent.CurrLoc.X = newX
		}
		fromCurrent.Direction = newDir
	}
}

// Check if a node has collided into a trail, wall, or another node.
func nodeHasCollided(oldX int, oldY int, newX int, newY int) bool {
	// Wall boundaries.
	if newX < 0 || newY < 0 || newX > BOARD_SIZE || newY > BOARD_SIZE {
		return true
	}
	// Collision with another player or trail.
	if board[newY][newX] != "" {
		return true
	}
	return false
}

// Renders the game.
func renderGame() {
	printBoard()
	// TODO: This is a disgusting, terrible hack to allow the Node layer to
	//       broadcast state updates. We should replace this with something
	//       that's actually reasonable.
	if gSO != nil {
		gSO.Emit("gameStateUpdate", board)
	} else {
		log.Println("gSO is null though")
	}
}

// Update peers with node's current location.
func intervalUpdate() {
	if isPlaying == false {
		return
	}

	for {
		var message *Message
		if isLeader() {
			message = &Message{IsLeader: true, DeadNodes: deadNodes, Node: *myNode, History: PeerHistory}
		} else {
			message = &Message{Node: *myNode}
		}

		nodeJson, err := json.Marshal(message)
		checkErr(err)
		sendPacketsToPeers(nodeJson)
		time.Sleep(intervalUpdateRate)
	}
}

func sendPacketsToPeers(payload []byte) {
	for _, node := range nodes {
		if node.Id != nodeId {
			data := send("Sending interval update to "+node.Id+" at ip "+node.Ip, payload)
			sendUDPPacket(node.Ip, data)
		}
	}
}

// Send data to ip via UDP.
func sendUDPPacket(ip string, data []byte) {
	// TODO a random port is picked since
	// we can't listen and read at the same time
	udpConn, err := net.Dial("udp", ip)
	checkErr(err)
	defer udpConn.Close()

	_, err = udpConn.Write(data)
	checkErr(err)
}

func listenUDPPacket() {
	localAddr, err := net.ResolveUDPAddr("udp", nodeAddr)
	checkErr(err)
	udpConn, err := net.ListenUDP("udp", localAddr)
	checkErr(err)
	defer udpConn.Close()

	buf := make([]byte, 1024)

	for {
		n, addr, err := udpConn.ReadFromUDP(buf)
		msg := receive("LU: Received packet from "+addr.String(), buf, n)
		data := msg.Payload
		var message Message
		var node Node
		err = json.Unmarshal(data, &message)
		checkErr(err)
		node = message.Node

		localLog("Received ", node)
		lastCheckin[node.Id] = time.Now()

		if message.IsLeader {
			localLog("deadNodes are: ", message.DeadNodes)
			for _, n := range message.DeadNodes {
				removeNodeFromList(n)
			}

			// Cache history info from the leader
			PeerHistory = message.History
			UpdateBoard()
		} else if isLeader() {
			log.Println("LU: Leader packing")
			// If I am the leader -> Update PeerHistory with message
			PeerHistory[message.Node.Id] = append(PeerHistory[message.Node.Id], message.Node.CurrLoc)
			log.Println("#Move by", message.Node.Id, " is ", len(PeerHistory[message.Node.Id]))
		}

		if message.IsDeathReport {
			aliveNodes = aliveNodes - 1
			log.Println("**** DEATH REPORT *** size is now ", strconv.Itoa(aliveNodes))
			if aliveNodes == 1 {
				// Oh wow, I'm the only one alive!
				if gSO != nil {
					gSO.Emit("victory")
					isPlaying = false
				}
			}
		}

		// Received a direction change from a peer.
		// Match the state of peer by predicting its path.
		if message.IsDirectionChange {
			for _, n := range nodes {
				if n.Id == message.Node.Id {
					updateLocationOfNode(n, &message.Node)
				}
			}
		}

		if err != nil {
			localLog("Error: ", err)
		}

		time.Sleep(400 * time.Millisecond)
	}
}

// Tell my beloved friends I have died.
func reportMySorrowfulDeath() {
	msg := &Message{IsDeathReport: true, Node: *myNode}
	msgJson, err := json.Marshal(msg)
	checkErr(err)
	sendPacketsToPeers(msgJson)
}

func notifyPeersDirChanged(direction string) {
	prevDirection := myNode.Direction

	// check if the direction change for node with the id
	if prevDirection != direction {
		localLog("Direction for ", nodeId, " has changed from ",
			prevDirection, " to ", direction)
		myNode.Direction = direction

		msg := &Message{IsDirectionChange: true, Node: *myNode}
		msgJson, err := json.Marshal(msg)
		checkErr(err)
		sendPacketsToPeers(msgJson)
	}
}

func isLeader() bool {
	return nodes[0].Id == nodeId
}

func hasExceededThreshold(nodeLastCheckin int64) bool {
	// TODO gotta check the math
	threshold := nodeLastCheckin + (700 * int64(time.Millisecond/time.Nanosecond))
	now := time.Now().UnixNano()
	return threshold < now
}

func handleNodeFailure() {
	if isPlaying == false {
		return
	}

	// only for regular node
	// check if the time it last checked in exceed CHECKIN_INTERVAL
	for {
		if isLeader() {

			localLog("Im a leader.")
			for _, node := range nodes {
				if node.Id != nodeId {
					if hasExceededThreshold(lastCheckin[node.Id].UnixNano()) {
						localLog(node.Id, " HAS DIED")
						// TODO tell rest of nodes this node has died
						// --> leader should periodically send out active nodes in the system
						// --> so here we just have to remove it from the nodes list.
						deadNodes = append(deadNodes, node.Id)
						localLog(len(deadNodes))
						removeNodeFromList(node.Id)
					}
				}
			}
		} else {

			localLog("Im a node.")
			// Continually check if leader is alive.
			leaderId := nodes[0].Id
			if hasExceededThreshold(lastCheckin[leaderId].UnixNano()) {
				localLog("LEADER ", leaderId, " HAS DIED.")
				removeNodeFromList(leaderId)
				// TODO: remove leader? or ask other peers first?
			}
		}
		time.Sleep(intervalUpdateRate)
	}
}

// LEADER: removes a dead node from the node list.
// TODO: Have to confirm if this works.
func removeNodeFromList(id string) {
	i := 0
	for i < len(nodes) {
		currentNode := nodes[i]
		if currentNode.Id == id {
			nodes = append(nodes[:i], nodes[i+1:]...)
		} else {
			i++
		}
	}
}

func leaderConflictResolution() {
	// as the referee of the game,
	// broadcast your game state for the current window to all peers
	// call sendUDPPacket
}

// Error checking. Exit program when error occurs.
func checkErr(err error) {
	if err != nil {
		localLog("error:", err)
		os.Exit(1)
	}
}

// For debugging
func printBoard() {
	for r, _ := range board {
		fmt.Print("[")
		for _, item := range board[r] {
			if item == "" {
				fmt.Print("__" + " ")
			} else {
				fmt.Print(item + " ")
			}
		}
		fmt.Print("]\n")
	}
}
