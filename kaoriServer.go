package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/KiritoNya/animeworld"
	"github.com/KiritoNya/database"
	eraiRaws "github.com/KiritoNya/erai-raws"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/mattn/go-sqlite3" // Import go-sqlite3 library
	logger "github.com/sirupsen/logrus"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

const animeInCorsoPathTorrent string = ""
const animeInCorsoPath string = ""
const logFilePath string = "RAID_log.json"
const telegramBotFile string = "bot.json"
const databaseLink string = "<username>:<password>@tcp(<host_ip>:<port>)/<database>"
const driverDb string = "mysql"

type botTelegram struct {
	Token string
	Me    int64
}

type animeReleasing struct {
	name    string
	site    string
	date    int
	episode int
	quality string
}

type Episode struct {
	animeName string
	episode   []float64
	finish    bool
	date      time.Time
	quality   string
	link      string
	Referer   string
	recap     bool
	double    bool
}

type lastCheckDate struct {
	AnimeInCorso int64
}

func init() {

	file, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		log.Fatal(err)
	}

	// Log as JSON instead of the default ASCII formatter.
	logger.SetFormatter(&logger.JSONFormatter{})

	// Output to stdout instead of the default stderr
	// Can be any io.Writer, see below for File example
	logger.SetOutput(file)

	err = database.InitDB(databaseLink, driverDb)
	if err != nil {
		panic(err)
	}
}

func main() {

	//Start gorutine
	go start()

	//Create server
	mux := &http.ServeMux{}
	mux.HandleFunc("/show", show)

	var handler http.Handler = mux
	//handler = LogRequestHandler(handler)

	s := &http.Server{
		Addr:           ":8089",
		Handler:        handler,
		ReadTimeout:    10 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	logger.WithField("function", "main").Info("Start server")

	s.ListenAndServe()
}

func show(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {

		ip := GetIP(r)

		type resp struct {
			Name    string
			Site    string
			Date    string
			Episode int
			Quality string
		}

		var resps []resp

		//Do query
		row, err := database.GetElements("SELECT * FROM animeInCorso ORDER BY date")
		if err != nil {
			logger.WithFields(logger.Fields{
				"ip": ip,
				"function": "Show",
			}).Error(err)
		}

		//Fetch results
		for row.Next() { // Iterate and fetch the records from result cursor
			var ar animeReleasing
			var r resp
			row.Scan(&ar.name, &ar.site, &ar.date, &ar.episode, &ar.quality)
			r.Name = ar.name
			r.Site = ar.site
			r.Date = time.Unix(int64(ar.date), 0).Local().Format(time.RFC1123)
			r.Episode = ar.episode
			r.Quality = ar.quality
			resps = append(resps, r)
		}
		row.Close()

		//Create JSON
		b, err := json.MarshalIndent(resps, "", "\t")
		if err != nil {
			logger.WithFields(logger.Fields{
				"ip": ip,
				"function": "Show",
			}).Error(err)
			PrintInternalErr(w)
		}

		//Write HTTP response
		w.Write(b)

		//Print log info
		logger.WithFields(logger.Fields{
			"ip": ip,
			"function": "Show",
		}).Info("Showed the item database")
	}
}

func start() {

	for range time.Tick(time.Minute * 10) {

		var ars []animeReleasing

		//Do query
		row, err := database.GetElements("SELECT * FROM animeInCorso where date<=" + strconv.FormatInt(time.Now().Unix(), 10))
		if err != nil {
			logger.WithField("function", "start").Error(err)
			continue
		}

		//Fetch results
		for row.Next() { // Iterate and fetch the records from result cursor
			var ar animeReleasing
			row.Scan(&ar.name, &ar.site, &ar.date, &ar.episode, &ar.quality)
			ars = append(ars, ar)
		}
		row.Close()

		logger.WithField("function", "Start").Info("SCANSION")

		for _, ar := range ars {
			err := ar.getNewAnime()
			if err != nil {
				logger.WithField("function", "Start").Error(ar.name, " Ep ", ar.episode, ": ", err)
				continue
			}
		}
	}
}

func (ar *animeReleasing) getNewAnime() error {

	logger.WithField("function", "GetNewAnime").Info("Check " + ar.name + " Ep " + strconv.Itoa(ar.episode))

	switch ar.site {
	case "erai-raws":

		var link eraiRaws.LinkMagnet

		switch ar.quality {
		case "1080p":
			link = eraiRaws.RssMagnetLink_1080
		case "720p":
			link = eraiRaws.RssMagnetLink_720
		case "480p":
			link = eraiRaws.RssMagnetLink_480
		}

		e, err := eraiRaws.NewRssMagnet(link)
		if err != nil {
			return err
		}

		ep, err := ar.eraiRawsFind(e)
		if err != nil {
			return err
		}

		//If anime not found
		if ep.animeName == "" {
			return nil
		}

		//Download
		if ep.double == true {
			logger.WithField("function", "GetNewAnime").Info("Download torrent ", ep.animeName, " Ep ", ep.episode[0], "-", +ep.episode[1], " [site:", ar.site, "]")
		} else {
			logger.WithField("function", "GetNewAnime").Info("Download torrent ", ep.animeName, " Ep ", ep.episode[0], " [site:", ar.site, "]")
		}

		err = ep.downloadTorrent()
		if err != nil {
			return err
		}

		//Update
		err = ar.update(&ep)
		if err != nil {
			return err
		}

	case "animeworld":
		switch ar.quality {
		case "720p":

			a, err := animeworld.NewRssAnimeworld()
			if err != nil {
				return err
			}

			ep, err := ar.animeworldFind(a)
			if err != nil {
				return err
			}

			//If anime not found
			if ep.animeName == "" {
				return nil
			}

			//Download
			if ep.double == true {
				logger.WithField("function", "GetNewAnime").Info("Download torrent ", ep.animeName, " Ep ", ep.episode[0], "-", +ep.episode[1], " [site:", ar.site, "]")
			} else {
				logger.WithField("function", "GetNewAnime").Info("Download torrent ", ep.animeName, " Ep ", ep.episode[0], " [site:", ar.site, "]")
			}
			err = ep.download()
			if err != nil {
				return err
			}

			//Update
			if ep.double == true {
				logger.WithField("function", "GetNewAnime").Info("Updating ", ep.animeName, " Ep ", ep.episode[0], "-", +ep.episode[1], " [site:", ar.site, "]")
			} else {
				logger.WithField("function", "GetNewAnime").Info("Updating", ep.animeName, " Ep ", ep.episode[0], " [site:", ar.site, "]")
			}

			err = ar.update(&ep)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (ar *animeReleasing) eraiRawsFind(e eraiRaws.RssMagnet) (ep Episode, err error) {
	for i := 0; i < e.GetNumberItems(); i++ {

		//Get title of RSS
		title := e.GetItemTitle(i)
		matrix := strings.Split(title, " – ")
		matrix2 := strings.Split(matrix[0], "] ")
		animeName := matrix2[1]

		if strings.Contains(animeName, ar.name) {
			ep.animeName = animeName
			ep.quality = strings.Replace(matrix2[0], "[", "", -1)
			matrix2 = strings.Split(matrix[len(matrix)-1], " ")
			episode := matrix2[0]
			if strings.Contains(episode, "v2") {
				episode = strings.Replace(episode, "v2", "", -1)
			}
			episodeFloat, err := strconv.ParseFloat(episode, 32)
			if err != nil {
				return Episode{}, err
			}
			if episodeFloat == float64(ar.episode) {
				ep.episode = append(ep.episode, episodeFloat)
				ep.recap = false
				ep.link = e.GetItemLink(i)
				ep.date = e.GetItemPubDate(i)
				if len(matrix2) == 2 {
					if matrix2[1] == "END" {
						ep.finish = true
					}
				}
				return ep, nil
			}
			if episodeFloat == float64(ar.episode)-0.5 {
				ep.episode = append(ep.episode, episodeFloat)
				ep.recap = true
				ep.link = e.GetItemLink(i)
				ep.date = e.GetItemPubDate(i)
				if len(matrix2) == 2 {
					if matrix2[1] == "END" {
						ep.finish = true
					}
				}
				return ep, nil
			}
		}
	}
	return Episode{}, nil
}

func (ar *animeReleasing) animeworldFind(a animeworld.RssAnimeworld) (ep Episode, err error) {
	for i := 0; i < a.GetNumberItems(); i++ {

		//Get title of RSS
		ep.animeName = a.GetItemAnimeName(i)

		//If title is same
		if strings.Contains(ep.animeName, ar.name) {

			//Get episode
			episode := a.GetItemEpisodeNumber(i)

			//Remove v2
			if strings.Contains(episode, "v2") {
				episode = strings.Replace(episode, "v2", "", -1)
			}

			//Check if it is double
			if a.GetItemEpisodeDouble(i) {
				ep.double = true
			}

			//for each episode
			for _, epElement := range strings.Split(episode, "-") {

				episodeFloat, err := strconv.ParseFloat(epElement, 32)
				if err != nil {
					return Episode{}, err
				}

				ep.episode = append(ep.episode, episodeFloat)

			}

			//Normal episode
			if ep.episode[0] == float64(ar.episode) {
				ep.recap = false

				//Create object newEpisode
				animeworldEp, err := animeworld.NewEpisode(a.GetItemLink(i))
				if err != nil {
					return Episode{}, err
				}

				//Set referer
				ep.Referer = animeworldEp.Link

				//Get direct video link
				err = animeworldEp.GetDirectLink()
				if err != nil {
					return Episode{}, err
				}
				ep.link = animeworldEp.DirectLink

				//Get date
				ep.date = a.GetItemPubDate(i)

				return ep, nil
			}

			//Recap episodes
			if ep.episode[0] == float64(ar.episode)-0.5 {
				ep.recap = true

				//Create object
				animeworldEp, err := animeworld.NewEpisode(a.GetItemLink(i))
				if err != nil {
					return Episode{}, err
				}

				//Create referer
				ep.Referer = animeworldEp.Link

				//Get direct link
				err = animeworldEp.GetDirectLink()
				if err != nil {
					return Episode{}, err
				}
				ep.link = animeworldEp.DirectLink

				//Get public date
				ep.date = a.GetItemPubDate(i)
				return ep, nil
			}
		}
	}
	return Episode{}, nil
}

func (ep *Episode) downloadTorrent() error {

	var send struct {
		Url      string
		Referer  string
		PathFile string
		Torrent  bool
		SizeRed  bool
	}

	link, err := url.Parse(ep.link)
	if err != nil {
		return err
	}
	q := link.Query()

	nameFile, err := url.PathUnescape(q["dn"][0])
	if err != nil {
		return err
	}

	//Generate path file
	path := strings.ToLower(animeInCorsoPathTorrent + strings.ToUpper(string(ep.animeName[0])) + "/" + ep.animeName + "/")

	//Add value to send struct
	send.PathFile = path
	send.Url = ep.link
	send.Referer = ""
	send.Torrent = true
	send.SizeRed = false

	//Create JSON
	jsonContent, err := json.Marshal(send)
	if err != nil {
		return err
	}

	//Send
	resp, err := http.Post("http://localhost:8090/add", "application/json", bytes.NewReader(jsonContent))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return errors.New("Error" + resp.Status)
	}

	logger.WithField("function", "download").Info("Add ", nameFile, "to the download queue")

	//Send message
	var t botTelegram

	err = t.SetToken()
	if err != nil {
		log.Println(err)
		return err
	}

	err = t.SendMessage("Add " + nameFile + " in the download torrent queue")
	if err != nil {
		log.Println(err)
		return err
	}

	logger.WithField("function", "DownloadTorrent").Info("Send telegram message")

	return nil
}

func (ep *Episode) download() error {

	var send struct {
		Url      string
		Referer  string
		PathFile string
		Torrent  bool
		SizeRed  bool
	}

	//Get name file
	nameFile := path.Base(ep.link)

	logger.WithField("function", "download").Info("Generate name file: ", nameFile)

	//Generate path file
	path := strings.ToLower(animeInCorsoPath + strings.ToLower(string(ep.animeName[0])) + "/" + ep.animeName + "/")

	logger.WithField("function", "download").Info("Generate path: ", path)

	//Add value to send struct
	send.PathFile = path + nameFile
	send.Url = ep.link
	send.Referer = ep.Referer
	send.Torrent = false
	send.SizeRed = false

	//Create JSON
	jsonContent, err := json.Marshal(send)
	if err != nil {
		return err
	}

	//Send
	resp, err := http.Post("http://localhost:8090/add", "application/json", bytes.NewReader(jsonContent))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return errors.New("Error" + resp.Status)
	}

	logger.WithField("function", "download").Info("Add ", nameFile, "to the download queue")

	//Send message
	var t botTelegram

	err = t.SetToken()
	if err != nil {
		return err
	}

	err = t.SendMessage("Add " + nameFile + " in the download queue")
	if err != nil {
		return err
	}

	logger.WithField("function", "download").Info("Telegram message sent")

	return nil
}

func (ep *Episode) createPath(prefix string) (string, error) {
	finalDest := prefix + strings.ToUpper(string(ep.animeName[0])) + "/" + ep.animeName + "/"
	os.MkdirAll(finalDest, 0644)
	return finalDest, nil
}

func (ar *animeReleasing) update(ep *Episode) error {
	if !ep.finish {
		if ep.recap == false {

			// update
			_, err := database.ChangeElement("update animeInCorso set episode=?, date=? where name=?", ar.episode+1, int(ep.date.Unix()+604800), ar.name)
			if err != nil {
				return err
			}
		}
	} else {
		logger.WithField("function", "Update").Info("Deleting ", ar.name, "...")

		// delete
		_, err := database.ChangeElement("delete from animeInCorso where name=?", ar.name)
		if err != nil {
			return err
		}

	}
	return nil
}

//Convert episode float to string for print in the log file
func (ep *Episode) sliceFloatToString() string {
	var episodePrint string
	if len(ep.episode)-1 == 2 {
		episodePrint = strconv.FormatFloat(ep.episode[0], 'E', -1, 64)
		episodePrint += "-"
		episodePrint += strconv.FormatFloat(ep.episode[0], 'E', -1, 64)
	} else {
		episodePrint = strconv.FormatFloat(ep.episode[0], 'E', -1, 64)
	}
	return episodePrint
}

func (t *botTelegram) SetToken() error {
	//Open file
	f, err := os.Open(telegramBotFile)
	if err != nil {
		return err
	}
	defer f.Close()

	content, err := ioutil.ReadAll(f)
	if err != nil {
		return err
	}

	err = json.Unmarshal(content, &t)
	if err != nil {
		return err
	}
	return nil
}

func (t *botTelegram) SendMessage(message string) error {

	type sendMessageReqBody struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}

	//Creates an instance of our custom sendMessageReqBody Type
	reqBody := &sendMessageReqBody{
		ChatID: t.Me,
		Text:   message,
	}

	// Convert our custom type into json format
	reqBytes, err := json.Marshal(reqBody)

	if err != nil {
		return err
	}

	// Make a request to send our message using the POST method to the telegram bot API
	resp, err := http.Post(
		"https://api.telegram.org/bot"+t.Token+"/"+"sendMessage",
		"application/json",
		bytes.NewBuffer(reqBytes),
	)

	if err != nil {
		return err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return errors.New("unexpected status" + resp.Status)
	}

	return nil
}

func PrintInternalErr(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusInternalServerError)
	w.Write([]byte("500 - Internal Server Error!\n"))
}

//Restituisce l'IP del client che ha effettuato la richiesta
func GetIP(r *http.Request) string {
	forwarded := r.Header.Get("X-FORWARDED-FOR")
	if forwarded != "" {
		return forwarded
	}
	return r.RemoteAddr
}
