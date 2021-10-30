package main

import (
	"encoding/json"
	"fmt"
	_ "github.com/aler9/gortsplib/pkg/headers"
	_ "github.com/nareix/joy4"
	"github.com/nareix/joy4/av/pubsub"
	"github.com/nareix/joy4/format"
	_ "github.com/pion/rtcp"
	"log"
	"net"

	"github.com/pion/webrtc"
	"net/http"
)

var rtpRemote *webrtc.RTPSender
var listener *net.UDPConn
var listeneraudio *net.UDPConn

func init() {
	format.RegisterAll()
}

var peerConnection *webrtc.PeerConnection //nolint

type Channel struct {
	que *pubsub.Queue
}

var channels = map[string]*Channel{}

// This example shows how to
// 1. create a RTSP server which accepts plain connections
// 2. allow a single client to publish a stream with TCP or UDP
// 3. allow multiple clients to read that stream with TCP or UDP

func IndexHandler(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "../view/index.html")
}

// doSignaling exchanges all state of the local PeerConnection and is called
// every time a video is added or removed
func doSignaling(w http.ResponseWriter, r *http.Request) {
	var offer webrtc.SessionDescription
	if err := json.NewDecoder(r.Body).Decode(&offer); err != nil {
		panic(err)
	}

	if err := peerConnection.SetRemoteDescription(offer); err != nil {
		panic(err)
	}

	// Create channel that is blocked until ICE Gathering is complete
	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)

	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		panic(err)
	} else if err = peerConnection.SetLocalDescription(answer); err != nil {
		panic(err)
	}

	// Block until ICE Gathering is complete, disabling trickle ICE
	// we do this because we only can exchange one signaling message
	// in a production application you should exchange ICE Candidates via OnICECandidate
	<-gatherComplete

	response, err := json.Marshal(*peerConnection.LocalDescription())
	if err != nil {
		panic(err)
	}

	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(response); err != nil {
		panic(err)
	}
}

// Add a single video track
func createPeerConnection(w http.ResponseWriter, r *http.Request) {
	if peerConnection.ConnectionState() != webrtc.PeerConnectionStateNew {
		log.Println("createPeerConnection called in non-new state ", peerConnection.ConnectionState())
	}

	doSignaling(w, r)
	fmt.Println("PeerConnection has been created")
}

// Add a single video track
func addVideo(w http.ResponseWriter, r *http.Request) {
	videoTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "video", "pion")
	audioTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "pion")
	if err != nil {
		panic(err)
	}
	rtpSender, err := peerConnection.AddTrack(videoTrack)
	if err != nil {
		panic(err)
	}

	rtpSenderAudio, err := peerConnection.AddTrack(audioTrack)
	if err != nil {
		panic(err)
	}
	// Read incoming RTCP packets
	// Before these packets are returned they are processed by interceptors. For things
	// like NACK this needs to be called.
	go func() {
		rtcpBuf := make([]byte, 1600)
		for {
			n, _, rtcpErr := rtpSender.Read(rtcpBuf)
			if rtcpErr != nil {
				log.Println(n, err)
				return
			}
			log.Println(n, err)
		}
	}()
	go func() {
		rtcpBuf := make([]byte, 1600)
		for {
			n, _, rtcpErr := rtpSenderAudio.Read(rtcpBuf)
			if rtcpErr != nil {
				log.Println(n, err)
				return
			}
			log.Println(n, err)
		}
	}()
	doSignaling(w, r)
	go writeVideoToTrack(videoTrack, audioTrack)

	fmt.Println("Video track has been added")
}

// Remove a single sender
func removeVideo(w http.ResponseWriter, r *http.Request) {
	if senders := peerConnection.GetSenders(); len(senders) != 0 {
		if err := peerConnection.RemoveTrack(senders[0]); err != nil {
			panic(err)
		}
	}

	doSignaling(w, r)
	fmt.Println("Video track has been removed")
}
func writeVideoToTrack(video *webrtc.TrackLocalStaticRTP, audio *webrtc.TrackLocalStaticRTP) {

	inboundRTPPacket := make([]byte, 1600)      // UDP MTU
	inboundRTPPacketAudio := make([]byte, 1600) // UDP MTU
	//ticker := time.NewTicker(time.Millisecond )
	go func() {
		for {
			if listeneraudio == nil {
				log.Println("listener audio is null")
			}
			n, adr, err := listeneraudio.ReadFrom(inboundRTPPacketAudio)
			log.Println(n, adr.String(), adr.Network(), err, "audio")

			if err != nil {
				panic(fmt.Sprintf("error during read: %s", err))
			}
			if n, err = audio.Write(inboundRTPPacketAudio[:n]); err != nil {
				fmt.Printf("Finish writing audio track: %s ", err, n)
				continue
			}

		}
	}()
	for {
		if listener == nil {
			log.Println("listener is null")
		}
		n, adr, err := listener.ReadFrom(inboundRTPPacket)
		log.Println(n, adr.String(), adr.Network(), err, "video")

		if err != nil {
			panic(fmt.Sprintf("error during read: %s", err))
		}
		if n, err = video.Write(inboundRTPPacket[:n]); err != nil {
			fmt.Printf("Finish writing video track: %s ", err, n)
			continue
		}

	}

}
func main() {

	var err error
	if peerConnection, err = webrtc.NewPeerConnection(webrtc.Configuration{}); err != nil {
		panic(err)
	}
	defer func() {
		if cErr := peerConnection.Close(); cErr != nil {
			fmt.Printf("cannot close peerConnection: %v\n", cErr)
		}
	}()

	// Set the handler for Peer connection state
	// This will notify you when the peer has connected/disconnected
	peerConnection.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		fmt.Printf("Peer Connection State has changed: %s\n", s.String())

		if s == webrtc.PeerConnectionStateFailed {
			// Wait until PeerConnection has had no network activity for 30 seconds or another failure. It may be reconnected using an ICE Restart.
			// Use webrtc.PeerConnectionStateDisconnected if you are interested in detecting faster timeout.
			// Note that the PeerConnection may come back from PeerConnectionStateDisconnected.
			fmt.Println("Peer Connection has gone to failed exiting")

		}
	})

	http.HandleFunc("/", IndexHandler)

	http.HandleFunc("/createPeerConnection", createPeerConnection)
	http.HandleFunc("/addVideo", addVideo)
	http.HandleFunc("/removeVideo", removeVideo)

	go func() {

		fmt.Println("Open http://localhost:8080 to access this demo")
		panic(http.ListenAndServe(":8080", nil))
	}()

	// Open a UDP Listener for RTP Packets on port 5004
	listener, err = net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("localhost"), Port: 5004})
	if err != nil {
		panic(err)
	}
	defer func() {
		if err = listener.Close(); err != nil {
			panic(err)
		}
	}()
	listeneraudio, err = net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("localhost"), Port: 5005})
	if err != nil {
		panic(err)
	}
	defer func() {
		if err = listeneraudio.Close(); err != nil {
			panic(err)
		}
	}()
	for {

	}
}
