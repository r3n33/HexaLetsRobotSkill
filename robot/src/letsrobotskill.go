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
	"mind/core/framework"

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

type ChatMessage struct {
	Message 	string 	`json:"message"`
	AnonUser	bool	`json:"anonymous"`
}

type Message struct {
    SendVideoProcessExists 	bool 	`json:"send_video_process_exists"`
    FFMPEGProcessExists 	bool 	`json:"ffmpeg_process_exists"`
    CameraID 				string 	`json:"camera_id"`
}

type LetsRobotSkill struct {
	skill.Base
	myClient *gosocketio.Client
	currentHeading float64
	currentHeight float64
	currentMoveSpeed int
	currentPitch float64
	allowAnonControl bool
	allowAnonChat bool

	depsFolder string

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

	batteryLevel int
	charging bool
}

func NewSkill() skill.Interface {
	return &LetsRobotSkill{}
}

func (d *LetsRobotSkill) OnStart() {
	d.LogSomethingInfo("OnStart()")

	d.depsFolder = "/var/local/mind/skills/LetsRobotSkill/deps/"

	d.allowAnonControl = false
	d.allowAnonChat = false
	d.streamingVideo = false
	d.timerRunning = false
	d.ffmpegPID = 0
	d.currentHeight = 50.0
	d.currentMoveSpeed = 500
	d.currentPitch = 0.0
	d.nextControlTime = time.Now().Add(100 * time.Millisecond)

	hexabody.Start()
	hexabody.Stand()
	hexabody.MoveHead(0, 0)
	infrared.Start()
}

func (d *LetsRobotSkill) OnClose() {
	d.LogSomethingInfo("OnClose()")

	d.Disconnect()

	hexabody.Relax()
	hexabody.Close()
	infrared.Close()
}

func (d *LetsRobotSkill) OnConnect() {
	d.LogSomethingInfo("OnConnect()")

	hexabody.MoveHead(0, 0)
	d.currentMoveSpeed = 500
}

func (d *LetsRobotSkill) OnDisconnect() {
	d.LogSomethingInfo("OnDisconnect()")
}

func (d *LetsRobotSkill) OnRecvJSON(data []byte) {
	d.LogSomethingInfo("OnRecvJSON()")

	var msg interface{}
	err := json.Unmarshal([]byte(data), &msg)
	message, _ := msg.(map[string]interface{})
	if err != nil {
		log.Error.Println(err)
	}
	if roboid, ok := message["robotid"].(string); ok {
		d.myRobotID = roboid
		d.LogSomethingInfo("OnRecvJSON d.myRobotID: " + roboid)
    }
	if camid, ok := message["cameraid"].(string); ok {
		d.myCameraID = camid
		d.LogSomethingInfo("OnRecvJSON d.myCameraID: " + camid)
    }

    d.getControlHostPort()
    d.getVideoPort()
    go d.connectSocketIO()
    go d.StartFFmpeg()
    go d.StatusIntervalFunction()
}

func (d *LetsRobotSkill) OnRecvString(data string) {
	d.LogSomethingInfo("OnRecvString()")

	switch data {
	case "disconnect":
		d.Disconnect()
	default:
		log.Warn.Println("OnRecvString received something unexpected: ", data)
	}
}

func (d *LetsRobotSkill) Disconnect() {
	d.LogSomethingInfo("Disconnect()")

	if d.timerRunning == true {
		d.intervalTimer.Stop()
	}

	if d.streamingVideo == true {
		d.KillFFmpeg()
	}
	
	d.LogSomethingInfo("Disconnect(): Closing socket.io connection")
	d.myClient.Close()
	d.LogSomethingInfo("Disconnect(): Socket.io connection closed")
}

func (d *LetsRobotSkill) LogSomethingInfo( info string ){
	log.Info.Println(info);
	framework.SendString(info)
}

func (d *LetsRobotSkill) LogSomethingError( info string ){
	log.Fatal.Println(info);
	framework.SendString(info)
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
		return
	}
	d.nextControlTime = time.Now().Add(100 * time.Millisecond)
	
	//d.LogSomethingInfo("HexaDoCommand(): " + cmd )
	switch cmd {
	case "F":
		hexabody.Walk( d.currentHeading, d.currentMoveSpeed )
	case "B":
		hexabody.Walk( HeadingWrap( d.currentHeading - 180.0 ), d.currentMoveSpeed )
	case "L":
		d.currentHeading = HeadingWrap( d.currentHeading + 5.0 )
		hexabody.MoveHead( d.currentHeading, d.currentMoveSpeed )
		if d.currentPitch != 0.0 {
			hexabody.Pitch(d.currentPitch, d.currentMoveSpeed)
		}
	case "R":
		d.currentHeading = HeadingWrap( d.currentHeading - 5.0 )
		hexabody.MoveHead( d.currentHeading, d.currentMoveSpeed )
		if d.currentPitch != 0.0 {
			hexabody.Pitch(d.currentPitch, d.currentMoveSpeed)
		}
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
	case "StartMarch":
		hexabody.StartMarching()
	case "StopMarch":
		hexabody.StopMarching()
	case "GaitOriginal":
		hexabody.SelectGait(hexabody.GaitOriginal)
	case "GaitWave":
		hexabody.SelectGait(hexabody.GaitOriginal)
		hexabody.SelectGait(hexabody.GaitWave)
	case "GaitRipple":
		hexabody.SelectGait(hexabody.GaitOriginal)
		hexabody.SelectGait(hexabody.GaitRipple)
	case "GaitTripod":
		hexabody.SelectGait(hexabody.GaitOriginal)
		hexabody.SelectGait(hexabody.GaitTripod)
	case "PitchUp":
		if d.currentPitch < 30.0 {
			d.currentPitch += 5.0
		}
		hexabody.Pitch(d.currentPitch, d.currentMoveSpeed)
	case "PitchDown":
		if d.currentPitch > -30.0 {
			d.currentPitch -= 5.0
		}	
		hexabody.Pitch(d.currentPitch, d.currentMoveSpeed)
	case "StopPitch":
		d.currentPitch = 0.0
		hexabody.StopPitch() //NOTE: StopPitch() does not auto return body to level
		hexabody.Pitch(d.currentPitch, d.currentMoveSpeed)
	case "HeightUp":
		if d.currentHeight < 100.0 {
			d.currentHeight += 10.0
		}
		hexabody.StandWithHeight( d.currentHeight )
	case "HeightDown":
		if d.currentHeight > 10.0 {
			d.currentHeight -= 10.0
		}	
		hexabody.StandWithHeight( d.currentHeight )
	case "StopHeight":
		d.currentHeight = 50.0
		hexabody.Stand()
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
			//d.LogSomethingInfo("SocketIO received: command_to_robot: ", args)

			if args.KeyPosition != "up" && (args.AnonUser == false || d.allowAnonControl == true ){
				d.HexaDoCommand( args.Command )
			}
		} 
	})
	if err != nil {
		log.Fatal.Println(err)
	}

	/* TODO: Chat support
	err = d.myClient.On("chat_message_with_name", func(h *gosocketio.Channel, args ChatMessage) {
		framework.SendString(args.Message)
	})
	if err != nil {
		log.Fatal.Println(err)
	}
	*/

	err = d.myClient.On(gosocketio.OnDisconnection, func(h *gosocketio.Channel) {
		log.Fatal.Println("Disconnected")
		if d.streamingVideo == true {
			d.LogSomethingInfo("Disconnected by error.. Will reconnect...")
			time.Sleep(time.Second)
			d.connectSocketIO()
			return
		}		
	})
	if err != nil {
		log.Fatal.Println(err)
	}

	err = d.myClient.On(gosocketio.OnConnection, func(h *gosocketio.Channel) {
		d.LogSomethingInfo("connectSocketIO(): Connected")
	})
	if err != nil {
		log.Fatal.Println(err)
	}
}

func (d *LetsRobotSkill) getControlHostPort() {
	d.LogSomethingInfo("d.getControlHostPort()")
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

	d.LogSomethingInfo( "getControlHostPort received: " + d.remoteURL + " on port:" + strconv.Itoa(d.remoteControlPort) )
}

func (d *LetsRobotSkill) getVideoPort() {
	d.LogSomethingInfo("d.getVideoPort()")
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

	d.LogSomethingInfo( "getVideoPort received port:" + strconv.Itoa(d.remoteVideoPort) )
}

func (d *LetsRobotSkill) KillFFmpeg() {
	d.LogSomethingInfo("KillFFmpeg()")

	d.streamingVideo = false
	if d.ffmpegPID != 0 {

		d.LogSomethingInfo("ffmpeg proces exists... killing ")
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
	d.LogSomethingInfo("StartFFmpeg()")

	d.streamingVideo = true
	for d.streamingVideo == true {
		serverurl := "http://letsrobot.tv:" + strconv.Itoa( d.remoteVideoPort ) + "/hello/640/480/"

		//TODO: overlayCommand := "-vf dynoverlay=overlayfile=/dev/shm/battery.png:check_interval=1000:x=0:y=0,dynoverlay=overlayfile=/dev/shm/charging.png:check_interval=1000:x=0:y=0 "

		argstring := "-i /dev/video0 -f mpegts -codec:v mpeg1video -s 640x480 -b:v 350k -bf 0 -muxdelay 0.001 " + serverurl //+ overlayCommand + serverurl

		d.LogSomethingInfo( "StartFFmpeg() connecting to: " + serverurl )

		d.LogSomethingInfo( "FFMPEG CMD: " + d.depsFolder + "local/bin/./ffmpeg " + argstring )

		args := strings.Split( argstring, " " )

		cmd := exec.Command( d.depsFolder + "local/bin/ffmpeg", args... )
		
		err := cmd.Start()

		if err != nil {
		    log.Fatal.Println( err )
		    return
		}

		d.LogSomethingInfo( "StartFFmpeg() running. Waiting for completion..." )

		d.ffmpegPID = cmd.Process.Pid

		cmd.Wait()

		d.ffmpegPID = 0

		time.Sleep(time.Second * 3)
	}

	d.LogSomethingInfo("d.streamingVideo == false...finished StartFFmpeg()")
}

func copyFile(srcFolder string, destFolder string){
	cpCmd := exec.Command("cp", "-rf", srcFolder, destFolder)
	err := cpCmd.Run()
	if err != nil {
	    log.Fatal.Println( err )
	    return
	}
}

func (d *LetsRobotSkill) GetIndexedBatteryLevel() int {
	
	if d.batteryLevel > 85 {
		return 85
	} else if d.batteryLevel > 50 {
		return 50
	} else if d.batteryLevel > 30 {
		return 30
	} else if d.batteryLevel > 10 {
		return 10
	}

	return 0
}

func (d *LetsRobotSkill) StatusIntervalFunction() {
	d.intervalTimer = time.NewTicker( time.Second )
	d.timerRunning = true
	counter := 0
	if d.streamingVideo == true {
		d.myClient.Emit("identify_robot_id", d.myCameraID)
	}
	
	lastcharging := !d.charging
	lastbattery := -1
	tensecondinterval := 0

	for _ = range d.intervalTimer.C {
		if d.charging != lastcharging {
			lastcharging = d.charging
			/*
			if d.charging {
				copyFile(  fmt.Sprintf( "%shud/charging.png", d.depsFolder ), "/dev/shm/charging.png" )
			} else {
				os.Remove( "/dev/shm/charging.png" )
			}
			*/
		}

		if d.GetIndexedBatteryLevel() != lastbattery {
			//copyFile(  fmt.Sprintf( "%shud/battery-%02d.png", d.depsFolder, d.GetIndexedBatteryLevel() ), "/dev/shm/battery.png" )
			lastbattery = d.GetIndexedBatteryLevel()
		}

		tensecondinterval += 1
		if tensecondinterval >= 10 {
			tensecondinterval = 0
		    if d.ffmpegPID != 0 {
		    	d.myClient.Emit("send_video_status", Message{SendVideoProcessExists:true, FFMPEGProcessExists:true, CameraID:d.myCameraID} )
				counter += 1
				if counter >= 6 {
					d.myClient.Emit("identify_robot_id", d.myCameraID)
					counter = 0
				}
		    }
		}
	}
}