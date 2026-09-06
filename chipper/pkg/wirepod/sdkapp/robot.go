package sdkapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/digital-dream-labs/hugh/grpc/client"
	"github.com/fforchino/vector-go-sdk/pkg/vector"
	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

var robots []Robot
var robotsMu sync.RWMutex
var inhibitCreation bool

type Robot struct {
	ESN               string
	GUID              string
	Target            string
	Vector            *vector.Vector
	BcAssumption      bool
	CamStreaming      bool
	EventStreamClient vectorpb.ExternalInterface_EventStreamClient
	EventsStreaming   bool
	StimState         float32
	ConnTimer         int32
	Ctx               context.Context
	Cancel            context.CancelFunc
}

func newRobot(serial string) (Robot, int, error) {
	inhibitCreation = true
	var RobotObj Robot

	// generate context
	RobotObj.Ctx, RobotObj.Cancel = context.WithCancel(context.Background())
	registered := false
	defer func() {
		if !registered {
			RobotObj.Cancel()
		}
	}()

	// find robot info in BotInfo
	matched := false
	for _, robot := range vars.BotInfo.Robots {
		if strings.EqualFold(serial, robot.Esn) {
			RobotObj.ESN = strings.TrimSpace(strings.ToLower(serial))
			RobotObj.Target = robot.IPAddress + ":443"
			matched = true
			if robot.GUID == "" {
				robot.GUID = vars.BotInfo.GlobalGUID
				RobotObj.GUID = vars.BotInfo.GlobalGUID
			} else {
				RobotObj.GUID = robot.GUID
			}
			// Authentication material must never enter the browser-accessible debug log.
			logger.Println("Connecting to " + serial)
		}
	}
	if !matched {
		inhibitCreation = false
		return RobotObj, 0, fmt.Errorf("error: robot not found in SDK info file")
	}

	// create Vector instance
	var err error
	RobotObj.Vector, err = vector.New(
		vector.WithTarget(RobotObj.Target),
		vector.WithSerialNo(RobotObj.ESN),
		vector.WithToken(RobotObj.GUID),
	)
	if err != nil {
		inhibitCreation = false
		return RobotObj, 0, err
	}

	// connection check
	_, err = RobotObj.Vector.Conn.BatteryState(context.Background(), &vectorpb.BatteryStateRequest{})
	if err != nil {
		inhibitCreation = false
		return RobotObj, 0, err
	}

	// create client for event stream
	RobotObj.EventStreamClient, err = RobotObj.Vector.Conn.EventStream(
		RobotObj.Ctx,
		&vectorpb.EventRequest{
			ListType: &vectorpb.EventRequest_WhiteList{
				WhiteList: &vectorpb.FilterList{
					// this will be used only for stimulation graph for now
					List: []string{"stimulation_info"},
				},
			},
		},
	)
	if err != nil {
		inhibitCreation = false
		return RobotObj, 0, err
	}
	RobotObj.CamStreaming = false
	RobotObj.EventsStreaming = false

	// we have confirmed robot connection works, append to list of bots
	robotsMu.Lock()
	robots = append(robots, RobotObj)
	registered = true
	robotIndex := len(robots) - 1
	robotsMu.Unlock()

	// begin inactivity timer
	go connTimer(RobotObj.ESN)

	inhibitCreation = false
	return RobotObj, robotIndex, nil
}

func getRobot(serial string) (Robot, int, error) {
	// look in robot list
	for {
		if !inhibitCreation {
			break
		}
		time.Sleep(time.Second / 2)
	}
	robotsMu.RLock()
	for index, robot := range robots {
		if strings.EqualFold(serial, robot.ESN) {
			robotsMu.RUnlock()
			return robot, index, nil
		}
	}
	robotsMu.RUnlock()
	return newRobot(serial)
}

// If connection is inactive for more than five minutes, remove the robot.
// The timer tracks the ESN rather than a slice index: removing one robot
// compacts the slice and can otherwise make a sibling timer panic or operate
// on the wrong robot.
func connTimer(serial string) {
	serial = strings.ToLower(strings.TrimSpace(serial))
	for {
		time.Sleep(time.Second)
		robotsMu.Lock()
		ind := robotIndexLocked(serial)
		if ind < 0 {
			robotsMu.Unlock()
			return
		}
		// A subscribed Hermes event stream is itself ongoing SDK activity. Keep
		// the connection alive so face-event delivery does not disappear after
		// five minutes of otherwise quiet robot time.
		if hermesEventsActive(serial) {
			robots[ind].ConnTimer = 0
			robotsMu.Unlock()
			continue
		}
		if robots[ind].ConnTimer >= 300 {
			serial = robots[ind].ESN
			robotsMu.Unlock()
			logger.Println("Closing SDK connection for " + serial + ", source: connTimer")
			removeRobot(serial)
			return
		}
		robots[ind].ConnTimer = robots[ind].ConnTimer + 1
		robotsMu.Unlock()
	}
}

func robotIndexLocked(serial string) int {
	for index, robot := range robots {
		if strings.EqualFold(serial, robot.ESN) {
			return index
		}
	}
	return -1
}

func removeRobot(serial string) {
	cliffStreams.stop(serial)
	inhibitCreation = true
	var newRobots []Robot
	removed := false
	robotsMu.Lock()
	for ind, robot := range robots {
		if !strings.EqualFold(serial, robot.ESN) {
			newRobots = append(newRobots, robot)
		} else {
			if robot.Cancel != nil {
				robot.Cancel()
			}
			robots[ind].CamStreaming = false
			robots[ind].EventsStreaming = false
			robots[ind].BcAssumption = false
			removed = true
		}
	}
	robots = newRobots
	robotsMu.Unlock()
	if removed {
		// Give event and camera streams time to stop after the registry no longer
		// exposes the connection. Do not hold robotsMu while waiting.
		time.Sleep(time.Second * 3)
	}
	inhibitCreation = false
}

func NewWP(serial string, useGlobal bool) (*vector.Vector, error) {
	var target, guid string
	if serial == "" {
		return nil, fmt.Errorf("serial string missing")
	}
	matched := false
	for _, robot := range vars.BotInfo.Robots {
		if strings.EqualFold(serial, robot.Esn) {
			matched = true
			target = robot.IPAddress + ":443"
			guid = robot.GUID
			break
		}
	}
	if !matched {
		logger.Println("serial did not match any bot in bot json")
		return nil, errors.New("serial did not match any bot in bot json")
	}
	c, err := client.New(
		client.WithTarget(target),
		client.WithInsecureSkipVerify(),
	)
	if err != nil {
		return nil, err
	}
	if err := c.Connect(); err != nil {
		return nil, err
	}
	return vector.New(
		vector.WithTarget(target),
		vector.WithSerialNo(serial),
		vector.WithToken(guid),
	)
}
