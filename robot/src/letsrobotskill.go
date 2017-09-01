package LetsRobotSkill

import (
	"os"
	"time"	
	"strings"
	"os/exec"
	"net/http"
	"io/ioutil"
	"strconv"
	"encoding/json"
	"fmt"

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
	remoteVideoPort int
	streamingVideo bool

	ffmpegPID int
	timerRunning bool
	intervalTimer *time.Ticker
	nextControlTime time.Time
}

func NewSkill() skill.Interface {
	return &LetsRobotSkill{}
}

func (d *LetsRobotSkill) OnStart() {
	log.Info.Println("OnStart()")

	d.streamingVideo = false
	d.timerRunning = false
	d.ffmpegPID = 0
	d.currentMoveSpeed = 500
	d.nextControlTime = time.Now().Add(100 * time.Millisecond)

	hexabody.Start()
	hexabody.Stand()
	hexabody.MoveHead(0, 0)
	infrared.Start()
}

func (d *LetsRobotSkill) OnClose() {
	log.Info.Println("OnClose()")

	d.Disconnect()

	hexabody.Relax()
	hexabody.Close()
	infrared.Close()
}

func (d *LetsRobotSkill) OnConnect() {
	log.Info.Println("OnConnect()")

	hexabody.MoveHead(0, 0)
	d.currentMoveSpeed = 500
}

func (d *LetsRobotSkill) OnDisconnect() {
	log.Info.Println("OnDisconnect()")
}

func (d *LetsRobotSkill) OnRecvJSON(data []byte) {
	log.Info.Println("OnRecvJSON()")

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

    log.Info.Println("d.getControlHostPort()")
    d.getControlHostPort()
    log.Info.Println("d.getVideoPort()")
    d.getVideoPort()

    log.Info.Println("d.connectSocketIO()")
    go d.connectSocketIO()
      
    log.Info.Println("d.StartFFmpeg()")
    go d.StartFFmpeg()

    log.Info.Println("d.SendLetsRobotStatus()")
	go d.SendLetsRobotStatus()
}

func (d *LetsRobotSkill) OnRecvString(data string) {
	log.Info.Println("OnRecvString()")

	switch data {
	case "disconnect":
		d.Disconnect()
	default:
		log.Warn.Println("OnRecvString received something unexpected: ", data)
	}
}

func (d *LetsRobotSkill) Disconnect() {
	log.Info.Println("Disconnect()")

	if d.timerRunning == true {
		d.intervalTimer.Stop()
	}

	if d.streamingVideo == true {
		d.KillFFmpeg()
	}
	
	log.Info.Println("Closing socket.io connection")
	d.myClient.Close()
	log.Info.Println("Socket.io connection closed")
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
	diff := d.nextControlTime.Sub(time.Now())
	if diff > 0 {
		log.Info.Println("HexaDoCommand() rate limited. Ignoring.")
		return
	}
	d.nextControlTime = time.Now().Add(100 * time.Millisecond)
	
	log.Info.Println("HexaDoCommand(): ", cmd)
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
		if d.currentMoveSpeed > 300 {
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
	case "StartMarch":
		hexabody.StartMarching()
	case "StopMarch":
		hexabody.StopMarching()
	default:
		log.Warn.Println("HexaDoCommand() received something unexpected: ", cmd)
	}
}

func (d *LetsRobotSkill) connectSocketIO() {
	fmt.Println("connectSocketIO(): Opening connection to: " + d.remoteURL + " port " + strconv.Itoa(d.remoteControlPort))

	thisClient, err := gosocketio.Dial(
		gosocketio.GetUrl(d.remoteURL, d.remoteControlPort, false),
		transport.GetDefaultWebsocketTransport(),
	)
	if err != nil {
		log.Fatal.Println(err)
		time.Sleep(time.Second)
		d.connectSocketIO()
		return
	}
	d.myClient = thisClient
	
	err = d.myClient.On("command_to_robot", func(h *gosocketio.Channel, args RobotCommand) {
		if d.myRobotID == args.RobotId {
			//log.Info.Println("SocketIO received: command_to_robot: ", args)

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
		if d.streamingVideo == true {
			log.Info.Println("Disconnected by error.. Will reconnect...")
			time.Sleep(time.Second)
			d.connectSocketIO()
			return
		}		
	})
	if err != nil {
		log.Fatal.Println(err)
	}

	err = d.myClient.On(gosocketio.OnConnection, func(h *gosocketio.Channel) {
		log.Info.Println("connectSocketIO(): Connected")
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
		d.remoteVideoPort = int(port)
    }

	log.Info.Println( "getVideoPort received port:" + strconv.Itoa(d.remoteVideoPort) )
}

func (d *LetsRobotSkill) KillFFmpeg() {
	log.Info.Println("KillFFmpeg()")

	d.streamingVideo = false
	if d.ffmpegPID != 0 {

		log.Info.Println("ffmpeg proces exists... killing ")
		p, err := os.FindProcess(d.ffmpegPID)
		
		if err != nil{
			log.Fatal.Println("no ffmpeg process found.")
			return
		}

		p.Signal(os.Interrupt)

		d.ffmpegPID = 0
	}
	time.Sleep(time.Second)
}

func (d *LetsRobotSkill) StartFFmpeg() {
	log.Info.Println("StartFFmpeg()")

	d.streamingVideo = true
	for d.streamingVideo == true {

		url := "http://letsrobot.tv:" + strconv.Itoa( d.remoteVideoPort ) + "/hello/640/480/"
		argstring := "-i /dev/video0 -f mpegts -codec:v mpeg1video -s 640x480 -b:v 350k -bf 0 -muxdelay 0.001 " + url

		log.Info.Println("StartFFmpeg() connecting to: " + url)

		args := strings.Split(argstring," ")
		cmd := exec.Command("/var/local/mind/skills/LetsRobotSkill/deps/local/bin/ffmpeg",args...)
		
		err := cmd.Start()

		if err != nil {
		    log.Fatal.Println(err)
		    return
		}

		log.Info.Println("StartFFmpeg() Had no errors. Waiting for completion..." )

		d.ffmpegPID = cmd.Process.Pid

		cmd.Wait()

		d.ffmpegPID = 0

		time.Sleep(time.Second * 3)
	}

	log.Info.Println("d.streamingVideo == false...finished StartFFmpeg()")
}

func (d *LetsRobotSkill) SendLetsRobotStatus() {
	d.intervalTimer = time.NewTicker( time.Second )
	d.timerRunning = true
	counter := 0
	if d.streamingVideo == true {
		d.myClient.Emit("identify_robot_id", d.myCameraID)
	}
	
	for _ = range d.intervalTimer.C {
	    if d.ffmpegPID != 0 {
	    	log.Info.Println("SendLetsRobotStatus()")
			d.myClient.Emit("send_video_status", "{'send_video_process_exists': True, 'ffmpeg_process_exists': True, 'camera_id':"+d.myCameraID);
			counter += 1
			if counter == 60 {
				d.myClient.Emit("identify_robot_id", d.myCameraID)
				counter = 0
			}
	    }
	}
}