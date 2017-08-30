package LetsRobotSkill

import (
	"net/http"
	"io/ioutil"
	"strconv"
	"encoding/json"

	"mind/core/framework/drivers/hexabody"
	"mind/core/framework/drivers/infrared"
	"mind/core/framework/log"
	"mind/core/framework/skill"

	"github.com/graarh/golang-socketio/transport"
	"github.com/graarh/golang-socketio"
)

type RobotCommand struct {
	RobotId 	string 	`json:"robot_id"`
	Command 	string 	`json:"command"`
	KeyPosition string 	`json:"key_position"`
	AnonUser	bool	`json:"anonymous"`
	UsersName	string 	`json:"username"`
}

type LetsRobotSkill struct {
	skill.Base
	myClient *gosocketio.Client
	currentHeading float64
	currentMoveSpeed int
	allowAnonControl bool

	myRobotID string
	myCameraID string
	remoteURL string
	remoteControlPort int
	remoteAudioPort int
}

func NewSkill() skill.Interface {
	return &LetsRobotSkill{}
}

func (d *LetsRobotSkill) OnStart() {
	d.currentMoveSpeed = 500
	hexabody.Start()
	hexabody.Stand()
	hexabody.MoveHead(0, 0)
	infrared.Start()
}

func (d *LetsRobotSkill) OnClose() {
	log.Info.Println("Closing socket.io connection")
	d.myClient.Close()
	log.Info.Println("Socket.io connection closed")

	hexabody.Relax()
	hexabody.Close()
	infrared.Close()
}

func (d *LetsRobotSkill) OnConnect() {
	hexabody.MoveHead(0, 0)
	d.currentMoveSpeed = 500
}

func (d *LetsRobotSkill) OnDisconnect() {

}

func (d *LetsRobotSkill) OnRecvJSON(data []byte) {
	var msg interface{}
	err := json.Unmarshal([]byte(data), &msg)
	message, _ := msg.(map[string]interface{})
	if err != nil {
		log.Error.Println(err)
	}
	if roboid, ok := message["robotid"].(string); ok {
		d.myRobotID = roboid
    }
	if camid, ok := message["cameraid"].(string); ok {
		d.myCameraID = camid
    }
    log.Info.Println("OnRecvJSON d.myRobotID:", d.myRobotID)
    log.Info.Println("OnRecvJSON d.myCameraID:", d.myCameraID)

    d.getVideoPort()
    d.getControlHostPort()
    d.connectSocketIO()
}

func (d *LetsRobotSkill) OnRecvString(data string) {
	switch data {
	case "disconnect":
		d.OnClose()
	default:
		log.Warn.Println("OnRecvString received something unexpected: ", data)
	}
}

func HeadingWrap( direction float64 ) float64 {
	if direction >= 360.0 {
		direction -= 360.0
	}
	if direction < 0.0 {
		direction += 360.0
	}
	return direction
}

func (d *LetsRobotSkill) HexaDoCommand( cmd string ) {
	switch cmd {
	case "F":
		hexabody.Walk( d.currentHeading, d.currentMoveSpeed )
	case "B":
		hexabody.Walk( HeadingWrap( d.currentHeading - 180.0 ), d.currentMoveSpeed )
	case "L":
		d.currentHeading = HeadingWrap( d.currentHeading + 5.0 )
		hexabody.MoveHead( d.currentHeading, d.currentMoveSpeed )
	case "R":
		d.currentHeading = HeadingWrap( d.currentHeading - 5.0 )
		hexabody.MoveHead( d.currentHeading, d.currentMoveSpeed )
	case "Faster":
		if d.currentMoveSpeed > 100 {
			d.currentMoveSpeed -= 100
		}
	case "Slower":
		if d.currentMoveSpeed < 1000 {
			d.currentMoveSpeed += 100
		}
	case "LightOn":
		infrared.LightOn()
	case "LightOff":
		infrared.LightOff()
	default:
		log.Warn.Println("HexaDoCommand received something unexpected: ", cmd)
	}
}

func (d *LetsRobotSkill) connectSocketIO() {

	log.Info.Println("connectSocketIO: Opening connection to: " + d.remoteURL + " port " + strconv.Itoa(d.remoteControlPort))

	//connect to server, you can use your own transport settings
	thisClient, err := gosocketio.Dial(
		gosocketio.GetUrl(d.remoteURL, d.remoteControlPort, false),
		transport.GetDefaultWebsocketTransport(),
	)

	if err != nil {
		log.Fatal.Println(err)
	}
	d.myClient = thisClient
	
	err = d.myClient.On("command_to_robot", func(h *gosocketio.Channel, args RobotCommand) {
		if d.myRobotID == args.RobotId {
			log.Info.Println("Received command_to_robot: ", args)

			if args.KeyPosition != "up" && (args.AnonUser == false || d.allowAnonControl == true ){
				d.HexaDoCommand( args.Command )
			}
		} 
	})
	if err != nil {
		log.Fatal.Println(err)
	}

	err = d.myClient.On(gosocketio.OnDisconnection, func(h *gosocketio.Channel) {
		log.Fatal.Println("Disconnected")
	})
	if err != nil {
		log.Fatal.Println(err)
	}

	err = d.myClient.On(gosocketio.OnConnection, func(h *gosocketio.Channel) {
		log.Info.Println("Connected")
	})
	if err != nil {
		log.Fatal.Println(err)
	}
}

func (d *LetsRobotSkill) getControlHostPort() {
	client := &http.Client{}
	req, err := http.NewRequest("GET", "https://letsrobot.tv/get_control_host_port/" + d.myRobotID, nil)
	if err != nil {
	  log.Error.Println(err)
	}

	req.Header.Add("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
	  log.Error.Println(err)
	}
	value,_ := ioutil.ReadAll(resp.Body)

	var msg interface{}
	err = json.Unmarshal(value, &msg)
	if err != nil {
		log.Error.Println(err)
	}
	message, _ := msg.(map[string]interface{})

	if host, ok := message["host"].(string); ok {
		d.remoteURL = host
	}
	if port, ok := message["port"].(float64); ok {
		d.remoteControlPort = int(port)
    }

	log.Info.Println( "getControlHostPort received: " + d.remoteURL + " on port:" + strconv.Itoa(d.remoteControlPort) )
}

func (d *LetsRobotSkill) getVideoPort() {
	client := &http.Client{}
	req, err := http.NewRequest("GET", "https://letsrobot.tv/get_video_port/" + d.myCameraID, nil)
	if err != nil {
	  log.Error.Println(err)
	}

	req.Header.Add("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
	  log.Error.Println(err)
	}
	value,_ := ioutil.ReadAll(resp.Body)

	var msg interface{}
	err = json.Unmarshal(value, &msg)
	if err != nil {
		log.Error.Println(err)
	}
	message, _ := msg.(map[string]interface{})

	if port, ok := message["mpeg_stream_port"].(float64); ok {
		d.remoteAudioPort = int(port)
    }

	log.Info.Println( "getVideoPort received port:" + strconv.Itoa(d.remoteAudioPort) )
}