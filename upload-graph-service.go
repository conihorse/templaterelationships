package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"aqwari.net/xml/xmltree"
	"github.com/Tkanos/gonfig"
	"github.com/beevik/etree"
	"github.com/hashicorp/go-retryablehttp"
	"github.com/segmentio/ksuid"
)

type commitReport struct {
	Cid                         string    `json:"cid"`
	VersionAsset                int       `json:"versionAsset"`
	ResourceMainID              string    `json:"resourceMainId"`
	ResourceMainDisplayName     string    `json:"resourceMainDisplayName"`
	ResourceType                string    `json:"resourceType"`
	TemplateType                string    `json:"templateType"`
	ResourceDescription         string    `json:"resourceDescription"`
	Status                      string    `json:"status"`
	VersionAssetLatest          int       `json:"versionAssetLatest"`
	VersionAssetLatestPublished int       `json:"versionAssetLatestPublished"`
	CreationTime                time.Time `json:"creationTime"`
	ModificationTime            time.Time `json:"modificationTime"`
	BranchName                  string    `json:"branchName"`
	CidProject                  string    `json:"cidProject"`
	ProjectName                 string    `json:"projectName"`
}

type validationReport []struct {
	ErrorType          string `json:"errorType"`
	ErrorText          string `json:"errorText"`
	ResourceMainID     string `json:"resourceMainId"`
	ResourceType       string `json:"resourceType"`
	ValidationSeverity string `json:"validationSeverity"`
}

type nodeDefinition struct {
	//uid                string   // unique id of this relationship (not used ATM)
	NodeName           string   // the filename of the template (not path)
	NodeLocation       string   // the relative path to the node file
	NodeID             string   // internal id of the template
	NodeHash           string   // the md5 hash of the local template
	NodeIsHead         int      // = 1 if node is at top/head
	NodeCommitOrder    int      // order in which the node should be commmitted to ckm [-1,n : unknown, 0-n order)
	NodeValidated      int      // [-1,0,1] - unknown, failed, succeeded
	NodeIsCommitted    int      // [-1,0,1] : unknown, failed, succeeded
	NodeCommitIntended int      // [-1,0,1,2] : unknown, no, commit, bump commit
	NodeChanged        int      // flag set if NodeHash different to ckm version [ -1,0,1,2 : unknown,not changed,changed,new ]
	NodeCID            string   // ckm citable identifier for the template (blank if template is new)
	NodeParentList     []string // list of parent template filenames
	NodeReleasedList   []string // list of template filenames with a "released version" of the node
	NodeStatusMessage  string   // if something goes wrong....
	NodeType           string   // for new assets committed to ckm (set in client -> commit)
	NodeProjectCID     string   // for new assets committed to ckm (set in client -> commit)
}

type sessionData struct {
	sessionConfig    configuration
	sessionID        string
	ChangesetFolder  string
	WuaNodes         []nodeDefinition // working structure holding nodes for proceessing and graph generation.
	mappedList       []string
	relationsetXML   []string
	nodeOrderList    []string // the order of commit (top down process)
	relationshipData map[string]int
	HTMLGraph        string
	userStateInfo    string
	StatusText       string
	ChangeDetail     string // added to the committed assets
	ProcessingStage  int    // [0,1,2,3,4] not started, finished precommit, finished precommit, started commit, finished commit
	IsError          bool
	NewAssetMetadata string // passed from client
	//ckmToken         string // passed from client, used to identify user to CKM/Repository
	authUser     string
	authPassword string
}

var gSessionDataList []*sessionData // global array of session data, shared across threads but only one will write to it..... i think....

type configuration struct {
	MirrorCkmPath      string
	ChangesetPath      string
	WorkingFolderPath  string
	Port               string
	TestDataPath       string
	HTMLGraphTemplate  string
	HTMLStatusTemplate string
	User               string
	Password           string
}

type status struct {
	Message         string
	ProcessingStage int
	IsError         bool
}

// Note: Don't store your key in your source code. Pass it via an
// environmental variable, or flag (or both), and don't accidentally commit it
// alongside your code. Ensure your key is sufficiently random - i.e. use Go's
// crypto/rand or securecookie.GenerateRandomKey(32) and persist the result.
//var store = sessions.NewCookieStore([]byte("essionkey"))

func buildStatusPage(data sessionData) string {

	return data.StatusText
}

func getSessionData(sessionID string) *sessionData {

	for _, data := range gSessionDataList {
		if data.sessionID == sessionID {
			return data
		}
	}
	return nil

}

func handler(w http.ResponseWriter, r *http.Request) {

	//var templateID string
	//var templateName string
	var changesetFolder string
	var thisSessionData sessionData

	params := strings.Split(r.RequestURI, ",")

	if len(params) < 1 {
		return
	}

	param0 := strings.ToLower(strings.Trim(params[0], "/")) // operation type

	if param0 == "favicon.ico" {
		return
	}

	// ------------------------ later stage / existing-session functions ------------------------ //

	if param0 == "precommit" {
		statusSessionID := params[1] // sessionID
		thisSessionData := getSessionData(statusSessionID)
		thisSessionData.ProcessingStage = 1 // started precommit

		//backup(thisSessionData.ChangesetFolder, WorkingFolderPath, )
		backupTicket(*thisSessionData)

		go precommitProcessing(thisSessionData.sessionConfig.MirrorCkmPath, thisSessionData)
		// hash existing assets and identify changed assets

		// validate the assets that need committing

		// get latest versions for existing assets that haven't changed

		// get all related assets that might be missing

		// build precommit report.
		fmt.Fprintln(w, sendStatusToBrowser(statusSessionID))
		return
	}

	if param0 == "commit" {
		statusSessionID := params[1] // sessionID
		thisSessionData := getSessionData(statusSessionID)

		if len(params) > 2 {
			metadataNewAssets := params[2]
			thisSessionData.NewAssetMetadata = metadataNewAssets
		}

		// TODO enable client edit of change message

		/* 		if len(params) > 3 {
		   			committext := params[3]
		   			thisSessionData.ChangeDetail = committext
		   		}
		*/
		thisSessionData.ProcessingStage = 3 // started commit
		go commitProcessing(thisSessionData.ChangesetFolder, thisSessionData.sessionConfig.MirrorCkmPath, thisSessionData)
		fmt.Fprintln(w, sendStatusToBrowser(statusSessionID))
		return
	}

	if param0 == "projects" {
		//fmt.Fprintln(w, sendStatusToBrowser(statusSessionID))

		status, projects := sendProjectsToBrowser()
		if status {
			fmt.Fprintln(w, projects)
		}

		return
	}

	if param0 == "status" {
		statusSessionID := params[1] // sessionID
		fmt.Fprintln(w, sendStatusToBrowser(statusSessionID))
		return
	}

	if param0 == "report" {
		statusSessionID := params[1] // sessionID
		fmt.Fprintln(w, sendReportToBrowser(statusSessionID))
		return
	}

	if param0 == "ticket-view-report-json" {
		var thisSessionData *sessionData

		if len(params) > 2 {
			thisSessionData = getSessionData(params[2]) // sessionid'
			if thisSessionData != nil {
				changesetFolder = thisSessionData.sessionConfig.ChangesetPath + "/" + params[1] // ticket'
				mapTicketTemplates2(thisSessionData.sessionConfig.MirrorCkmPath, thisSessionData)
				getHashAndChangedStatus2(thisSessionData, true)
				walktree(thisSessionData)
				printMap(*thisSessionData)
				//fmt.Fprintln(w, sendWUVToBrowser(thisSessionData.sessionID))
				fmt.Fprintln(w, sendReportToBrowser(thisSessionData.sessionID))
			} else {
				setSessionFailure("ticket-view-report-json: couldn't get SessionData (check session id is passed in on uri)", thisSessionData)
			}
		}

		return
	}

	// ------------------------ new-session functions ------------------------ //

	thisSessionData.sessionConfig = configuration{}
	err := gonfig.GetConf("config.json", &thisSessionData.sessionConfig)
	if err != nil {
		panic(err)
	}
	thisSessionData.WuaNodes = make([]nodeDefinition, 0)
	thisSessionData.relationshipData = make(map[string]int) // where-used map
	thisSessionData.sessionID = ksuid.New().String()
	thisSessionData.ProcessingStage = 0
	thisSessionData.IsError = false
	thisSessionData.relationsetXML = []string{} // used to store the relationships between files
	thisSessionData.nodeOrderList = []string{}  // used to store the order of the commit (filename)
	gSessionDataList = append(gSessionDataList, &thisSessionData)

	log.Printf("ckmpath = " + (string)(thisSessionData.sessionConfig.MirrorCkmPath))

	switch {

	case param0 == "init":
		changesetFolder = thisSessionData.sessionConfig.ChangesetPath + "/" + params[1] // ticket'
		thisSessionData.ChangesetFolder = changesetFolder
		if checkEnvironment(thisSessionData) == false {
			setSessionFailure("Exiting due to environment/config issues...", &thisSessionData)
			return
		}

		template, err := readLines(thisSessionData.sessionConfig.HTMLStatusTemplate)

		if len(params) < 5 {
			setSessionFailure("Exiting due to lack of parameters passed in (check change detail * auth token)...", &thisSessionData)
			return

		}

		if err == nil {
			if len(params) > 2 {
				str := params[2]
				log.Println("base64 change detail = " + thisSessionData.ChangeDetail)
				data, err := base64.StdEncoding.DecodeString(str)
				if err != nil {
					fmt.Println("error:", err)
					return
				}
				fmt.Printf("%q\n", data)
				thisSessionData.ChangeDetail = string(data)
				log.Println("change detail = " + thisSessionData.ChangeDetail)
			}

			if len(params) > 4 {
				sUser := params[3] // user
				sPW := params[4]   // password

				data, err := base64.StdEncoding.DecodeString(sPW)
				if err != nil {
					fmt.Println("pw decode error:", err)
					return
				}
				fmt.Printf("%q\n", data)
				thisSessionData.authPassword = string(data)

				data, err = base64.StdEncoding.DecodeString(sUser)
				if err != nil {
					fmt.Println("error:", err)
					return
				}
				fmt.Printf("%q\n", data)
				thisSessionData.authUser = string(data)

				/* 				thisSessionData.ckmToken = str
				   				log.Println("ckm token = " + thisSessionData.ckmToken)
				*/
			}

			var line string
			for i := range template {
				line = template[i]
				line = strings.Replace(line, "%%TICKET%%", changesetFolder, -1)
				line = strings.Replace(line, "%%SESSIONID%%", thisSessionData.sessionID, -1)
				line = strings.Replace(line, "%%CHANGETEXT%%", thisSessionData.ChangeDetail, -1)
				line = strings.Replace(line, "%%DOCUMENTLOAD%%", "", -1)

				fmt.Fprintln(w, line)
			}
		}

		return

	case param0 == "get-related":
		thisSessionData.ChangesetFolder = thisSessionData.sessionConfig.ChangesetPath + "/" + params[1]
		//thisSessionData.ckmToken = params[2]
		if checkEnvironment(thisSessionData) == false {
			setSessionFailure("Exiting due to environment/config issues...", &thisSessionData)
			return
		}

		mapTicketTemplates(thisSessionData.sessionConfig.MirrorCkmPath, &thisSessionData)
		// build map for ticket
		var TicketWorkingFolderPath = thisSessionData.ChangesetFolder + "/" + thisSessionData.sessionConfig.WorkingFolderPath

		// for each asset that is missing, download it
		// (we dont need to walk the tree, just iterate the notes list)
		files := getLocalTemplateList(thisSessionData.ChangesetFolder)

		for _, node := range thisSessionData.WuaNodes {

			found := false

			for _, file := range files {
				log.Println(file)
				if strings.Contains(file, node.NodeName) {
					found = true
					break
				}
			}

			if !found {
				// need to download it
				fmt.Fprintf(w, "<h3>grabbing "+node.NodeName+"</h3>")

				templateid := node.NodeID
				// get cid

				templateexists, cid := ckmGetCidFromID(templateid, &thisSessionData)

				if templateexists {

					log.Printf("id: " + templateid + " cid: " + cid)
					// get template filepack url

					filepack := ckmGetTemplateFilepackURL(cid, &thisSessionData)
					log.Printf("filepack = " + filepack)
					// retrieve filepack
					filepackname := TicketWorkingFolderPath + "/" + cid + ".zip"
					err := ckmDownloadFile(filepackname, filepack)
					if err != nil {
						panic(err)
					}

					// unpack filepack

					err = unzip(filepackname, TicketWorkingFolderPath+"/unzipped")
					if err != nil {
						panic(err)
					}
				} else {
					log.Println("parseParentsTree : template doesn't exist : " + templateid)
				}
			}

		}
		status := ""

		status = moveFiles(thisSessionData.ChangesetFolder, "templates", thisSessionData.sessionConfig.WorkingFolderPath)
		log.Printf(status)
		fmt.Fprintf(w, "<h3>grabbed"+status+"</h3>")

		status = moveFiles(thisSessionData.ChangesetFolder, "archetypes", thisSessionData.sessionConfig.WorkingFolderPath)
		log.Printf(status)
		fmt.Fprintf(w, "<h3>grabbed"+status+"</h3>")

		// before downloading, back it up

	case param0 == "get-map":

		thisSessionData.ChangesetFolder = thisSessionData.sessionConfig.ChangesetPath + "/" + params[1]
		if checkEnvironment(thisSessionData) == false {
			setSessionFailure("Exiting due to environment/config issues...", &thisSessionData)
			return
		}
		template, err := readLines(thisSessionData.sessionConfig.HTMLStatusTemplate)
		//template, err := readLines("WURtemplate.html")

		if err == nil {
			var line string
			for i := range template {
				line = template[i]
				line = strings.Replace(line, "%%TICKET%%", thisSessionData.ChangesetFolder, -1)
				line = strings.Replace(line, "%%SESSIONID%%", thisSessionData.sessionID, -1)
				line = strings.Replace(line, "%%CHANGETEXT%%", thisSessionData.ChangeDetail, -1)
				line = strings.Replace(line, "%%DOCUMENTLOAD%%", "window.addEventListener('DOMContentLoaded', get_graph);", -1)
				fmt.Fprintln(w, line)
			}
		}
		//		fmt.Fprintln(w, template)
		return

	case param0 == "template-xml-report":
		for v := range thisSessionData.relationsetXML {
			fmt.Fprintf(w, thisSessionData.relationsetXML[v])
		}
		return

	default:
		log.Printf("unknown operation type: " + param0)
		log.Printf("exiting...")
		return
	}

}
func setSessionFailure(status string, data *sessionData) {
	log.Println("---------------------------> setSessionFailure : " + status)
	data.StatusText = status
	data.IsError = true
}
func updateSessionStatus(status string, data *sessionData) {
	log.Println(status)
	data.StatusText = status
}
func getNodeTypeAndProject(node nodeDefinition, metadata string) (status bool, assettype, project string) {
	status = false

	splits := strings.Split(strings.ToLower(metadata), "%5e")
	if len(splits) > 1 {

		for _, def := range splits {
			nodedef := strings.Split(def, "~")
			log.Println("found : " + nodedef[0])
			if nodedef[0] == node.NodeID {
				assettype = nodedef[1]
				project = nodedef[2]
				status = true
			}
		}
	}

	assettype = strings.ToUpper(strings.Replace(assettype, "%20", "_", 1))

	log.Println("asset type = " + assettype)
	log.Println("project cid = " + project)

	return status, assettype, project
}
func commitProcessing(changesetFolder string, mirrorpath string, data *sessionData) bool {

	updateSessionStatus("Committing assets", data)

	commitidx := 1
	found := false
	problems := false

	for ok := true; ok; ok = found && !problems {
		found = false
		for idx, node := range data.WuaNodes {
			if node.NodeCommitOrder == commitidx { // commit nodes in the correct order
				// commit this node
				log.Println("going to commit this: " + node.NodeName)

				if node.NodeValidated < 0 {
					if !ckmValidateTemplate(&data.WuaNodes[idx], data) {
						data.WuaNodes[idx].NodeValidated = 0
						setSessionFailure("commitProcessing : ERROR validate template failed for "+data.WuaNodes[idx].NodeName, data)
						problems = true
					}
					data.WuaNodes[idx].NodeValidated = 1
				}

				if node.NodeChanged == 2 {
					// new ndoe

					found, assettype, cid := getNodeTypeAndProject(node, data.NewAssetMetadata)

					problems = !found

					if !problems {
						data.WuaNodes[idx].NodeType = assettype
						data.WuaNodes[idx].NodeProjectCID = cid

						if !ckmCommitNewTemplate(&data.WuaNodes[idx], data) {
							problems = true
						}
						log.Println("New: " + node.NodeName)
					}

				} else {
					// existing node
					if !ckmCommitRevisedTemplate(&data.WuaNodes[idx], data) {
						problems = true
					}
					log.Println("update: " + node.NodeName)

				}

				if !problems {
					err := ckmGetTemplateOET(node, node.NodeLocation)
					if err != nil {
						setSessionFailure("ERROR: failed to get copy of committed template from CKM", data)
						problems = true
					}
				} else {
					setSessionFailure("ERROR : something went wrong in commit", data)
					return problems
				}

				data.WuaNodes[idx].NodeIsCommitted = 1
				commitidx++

				found = true
			}
		}

	}

	data.ProcessingStage = 4 // finished commit
	updateSessionStatus("*** Done! ***", data)
	data.HTMLGraph = generateMap2(*data, "graphtemplate.html")
	return problems
}

func precommitProcessing( /* changesetFolder string, */ mirrorpath string, data *sessionData) {

	updateSessionStatus("Building template maps", data)

	mapTicketTemplates2( /* "./"+data.ChangesetFolder+"/", */ mirrorpath, data)

	updateSessionStatus("Getting template metadata from CKM (hashes, cids, etc)", data)

	getHashAndChangedStatus2(data, false)
	walktree(data)
	setCommitOrder(data)

	log.Println(data.nodeOrderList)
	data.ProcessingStage = 2
	return
}

func walktree( data *sessionData) {

	// walk down tree, starting at each head node
	for i := 0; i < len(data.WuaNodes); i++ {
		// for each leaf, follow the tree up
		if len(data.WuaNodes[i].NodeParentList) == 0 {

			treeorder := []string{}

			log.Println("precommitProcessing: processTreeTopFirst( " + data.WuaNodes[i].NodeName + ")")
			if processTreeTopFirst(&data.WuaNodes[i], true, &treeorder, data, true) { // dry run flag set
				if data.WuaNodes[i].NodeCommitIntended < 1 {
					data.WuaNodes[i].NodeCommitIntended = 2
				}
			}
			mergeTraverseList(treeorder, data)
		}
	}
}

func setCommitOrder(data *sessionData) {

	// for each node in the nodeOrderList

	// check if it is to be committed, if so set its order

	order := 1

	for _, node := range data.nodeOrderList {
		for idx, nodedef := range data.WuaNodes {
			if node == nodedef.NodeName {
				if nodedef.NodeCommitIntended > 0 || nodedef.NodeChanged > 0 {
					//nodedef.NodeCommitOrder = order
					data.WuaNodes[idx].NodeCommitOrder = order
					order++
				}
			}
		}
	}
}

func mergeTraverseList(treeorder []string, data *sessionData) {

	// merge tree order into session commit order
	last := len(treeorder) - 1
	for i := range treeorder {

		node := treeorder[last-i]
		// for each node, check if it exists
		nodeExists := false
		for _, name := range data.nodeOrderList {
			if name == node {
				nodeExists = true
			}
		}

		// if it doesnt exist, insert it after its predecessor (which should already exist)
		if !nodeExists {
			insertpos := 0
			if i > 0 { // node has a predecessor
				prenode := treeorder[(last - i + 1)]

				for prepos, name := range data.nodeOrderList {
					if name == prenode {
						insertpos = prepos + 1
						break
					}
				}
			}
			// insert node into session at insertpos
			data.nodeOrderList = append(data.nodeOrderList, "")
			copy(data.nodeOrderList[(insertpos+1):], data.nodeOrderList[insertpos:])
			log.Println("inserting " + node + " at " + strconv.Itoa(insertpos))

			data.nodeOrderList[insertpos] = node
		}

	}
}

func checkEnvironment(data sessionData) bool { //config configuration, ticketdir string) bool {

	// check that the dam folder is there
	// check that the ticket folder is inside the dam folder
	// make the working folder inside the ticket, if it's not there...

	os.MkdirAll(data.ChangesetFolder+"/"+data.sessionConfig.WorkingFolderPath, os.ModePerm)

	return true
}

func moveFiles(changesetFolder, assetType, WorkingFolderPath string) string {

	cmd := exec.Command("rsync", "-av", "--ignore-existing", "--remove-source-files", changesetFolder+"/"+WorkingFolderPath+"/unzipped/"+assetType, changesetFolder)
	var outbuf, errbuf bytes.Buffer
	cmd.Stdout = &outbuf
	cmd.Stderr = &errbuf

	err := cmd.Run()
	if err != nil {
		log.Printf("moveFiles finished with error: %v", err)
	}

	stdout := outbuf.String()
	return stdout
}

func ckmGetTemplateFilepackURL(cid string, data *sessionData) (filesetURL string) {

	if contentdata, err := ckmGetContentPlain("https://ahsckm.ca/ckm/rest/v1/templates/"+cid+"/file-set-url", data); err != nil {
		log.Printf("Failed to get XML: %v", err)
	} else {
		//check(err)
		log.Println("Received XML:" + string(contentdata))
		return string(contentdata)
	}
	return ""
}

func cacheGetCidFromID(id string, data *sessionData) (status bool, cid string) {

	// TODO implement local cache
	//	return true, "fake"
	status, cid = ckmGetCidFromID(id, data)
	return status, cid
}

func ckmGetCidFromID(id string, data *sessionData) (status bool, cid string) {

	req, err := retryablehttp.NewRequest("GET", "https://ahsckm.ca/ckm/rest/v1/templates/citeable-identifier/"+id, nil)
	if err != nil {
		return false, ""
	}
	req.Header.Set("Accept", "text/plain")
	//req.Header.Set("Authorization", "Basic "+token)

	req.SetBasicAuth(data.authUser, data.authPassword)
	//req.Header.Set("JSESSIONID", token)
	client := retryablehttp.NewClient()
	client.CheckRetry = defaultRetryPolicy
	resp, err := client.Do(req)
	if err != nil {
		return false, ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 || resp.StatusCode == 404 { // not success
		if resp.StatusCode != 404 {
			log.Println("ERROR: ckmGetCidFromID response statuscode = " + strconv.FormatInt(int64(resp.StatusCode), 10))
		}
		return false, ""
	}

	bodydata, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		println()
		return false, ""
	}

	return true, string(bodydata)
}

func parseParentsTree(ParentTree []string, TicketWorkingFolderPath string, data *sessionData) {

	stringByte := strings.Join(ParentTree, "\x20") // x20 = space and x00 = null
	root, err := xmltree.Parse([]byte(stringByte))
	if err != nil {
		log.Fatal(err)
	}
	for _, el := range root.Search("", "id") {

		templateid := (string)(el.Content)
		// get cid

		templateexists, cid := ckmGetCidFromID(templateid, data)

		if templateexists {

			log.Printf("id: " + templateid + " cid: " + cid)
			// get template filepack url

			filepack := ckmGetTemplateFilepackURL(cid, data)
			log.Printf("filepack = " + filepack)
			// retrieve filepack
			filepackname := TicketWorkingFolderPath + "/" + cid + ".zip"
			err := ckmDownloadFile(filepackname, filepack)
			if err != nil {
				panic(err)
			}

			// unpack filepack

			err = unzip(filepackname, TicketWorkingFolderPath+"/unzipped")
			if err != nil {
				panic(err)
			}
		} else {
			log.Println("parseParentsTree : template doesn't exist : " + templateid)
		}
	}

}

/* func check(e error) {
	if e != nil {
		panic(e)
	}
}
*/
func backup(changesetFolder, WorkingFolderPath string, relation nodeDefinition) string {

	cmd := exec.Command("rsync", "-av", "--ignore-existing", "--remove-source-files", changesetFolder+"/"+WorkingFolderPath+"/backup/", relation.NodeLocation)
	var outbuf, errbuf bytes.Buffer
	cmd.Stdout = &outbuf
	cmd.Stderr = &errbuf

	err := cmd.Run()
	if err != nil {
		log.Printf("backup finished with error: %v", err)
	}

	stdout := outbuf.String()
	return stdout

}

func ckmDownloadFile(filepath string, url string) error {
	// DownloadFile will download a url to a local file. It's efficient because it will
	// write as it downloads and not load the whole file into memory.

	// Create the file
	out, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer out.Close()

	// Get the data
	client := retryablehttp.NewClient()
	client.CheckRetry = defaultRetryPolicy

	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Write the body to file
	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return err
	}

	return nil
}

func ckmGetContentPlain(url string, data *sessionData) ([]byte, error) {
	req, err := retryablehttp.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("ckmGetContentPlain: http.NewRequest() failed:  %v", err)
	}
	req.Header.Set("Accept", "text/plain")
	//req.Header.Set("Authorization", "Basic "+token)
	//req.Header.Set("JSESSIONID", token)
	req.SetBasicAuth(data.authUser, data.authPassword)

	client := retryablehttp.NewClient()
	client.CheckRetry = defaultRetryPolicy
	resp, err := client.Do(req)

	if err != nil {
		return nil, fmt.Errorf("ckmGetContentPlain:  http.DefaultClient.Do() failed: %v", err)
	}

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ckmGetContentPlain: Read body: %v", err)
	}
	return body, nil
}

func ckmGetContentXML(url string, data *sessionData) ([]byte, error) {
	req, err := retryablehttp.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("ckmGetContentXML: http.NewRequest() failed:  %v", err)
	}
	req.Header.Set("Accept", "application/xml")
	//	req.Header.Set("Authorization", "Basic "+token)
	//req.Header.Set("JSESSIONID", token)
	req.SetBasicAuth(data.authUser, data.authPassword)

	client := retryablehttp.NewClient()
	client.CheckRetry = defaultRetryPolicy
	resp, err := client.Do(req)

	if err != nil {
		return nil, fmt.Errorf("ckmGetContentXML:  http.DefaultClient.Do() failed: %v", err)
	}

	defer resp.Body.Close()
	bodydata, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ckmGetContentXML: Read body: %v", err)
	}
	return bodydata, nil
}

func loadTestData(path string, data *sessionData) []string {

	var files []string

	root := path
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {

		if strings.ToLower(filepath.Ext(info.Name())) == ".xml" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		panic(err)
	}
	for _, file := range files {
		log.Printf("testdata: " + file)
		testdata, err := readLines(file)
		if err == nil {
			mapWhereUsedXML(testdata, data)
		}
	}

	return nil
}

func unzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer func() {
		if err := r.Close(); err != nil {
			panic(err)
		}
	}()

	os.MkdirAll(dest, 0755)

	// Closure to address file descriptors issue with all the deferred .Close() methods
	extractAndWriteFile := func(f *zip.File) error {
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer func() {
			if err := rc.Close(); err != nil {
				panic(err)
			}
		}()

		path := filepath.Join(dest, f.Name)

		if f.FileInfo().IsDir() {
			os.MkdirAll(path, f.Mode())
		} else {
			os.MkdirAll(filepath.Dir(path), 0755)
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
			if err != nil {
				return err
			}
			defer func() {
				if err := f.Close(); err != nil {
					panic(err)
				}
			}()

			_, err = io.Copy(f, rc)
			if err != nil {
				return err
			}
		}
		return nil
	}

	for _, f := range r.File {
		err := extractAndWriteFile(f)
		if err != nil {
			return err
		}
	}

	return nil
}

func panic(err error) {
	fmt.Println(err.Error())

}

func main() {

	http.HandleFunc("/", handler)
	fmt.Println("Starting....")

	configuration := configuration{}
	err := gonfig.GetConf("config.json", &configuration)
	if err != nil {
		panic(err)
	}
	fmt.Println("Ready. Listening on port " + configuration.Port)

	http.ListenAndServe(":"+configuration.Port, nil)

}

func findTemplateID(path string) string {
	// return the unique identifier for the template specified in the path param

	var templateID string
	lines, err := readLines(path)
	if err != nil {
		log.Println("ERROR findTemplateID : " + err.Error())
		return templateID

	}
	content := strings.Join(lines, " ")
	splits := strings.Split(strings.ToLower(content), "<id>")
	if len(splits) > 1 {
		templateID = (splits[1])[0:36] // assumes id format is fixed.... naughty naughty
	}

	return templateID

}

// find names of templates that contain id
func findParentTemplates(id string, file string, ckmMirror string /* ticketDir string,  */, data *sessionData) bool {

	ticketDir := "./" + data.ChangesetFolder + "/"
	//	ckmMirror = "./"+ckmMirror+"/"

	if id == "" {
		log.Printf("findParentTemplates failure....no id passed in")
		return false
	}

	log.Printf("findParentTemplates( " + id + " / " + file)

	var foundfiles = grepDir("template_id=\""+id, ckmMirror)
	var foundlocalfiles = grepDir("template_id=\""+id, ticketDir)
	results := strings.Split(foundlocalfiles+"\n"+foundfiles, "\n")

	var foundversions = grepDir("{AHSID~"+id, ckmMirror) // TODO move traceability token to .config
	var foundlocalversions = grepDir("{AHSID~"+id, ticketDir)
	versions := strings.Split(foundlocalversions+"\n"+foundversions, "\n")

	data.relationsetXML = append(data.relationsetXML, "<template><filename>"+file+"</filename><id>"+id+"</id><contained-in>")

	parent := ""

	for i := range results {
		result := results[i]
		parts := strings.Split(result, ":")
		parent = parts[0]
		parent = strings.TrimSpace(parent)

		if parent != "" {
			log.Println("findParentTemplates parent - " + parent)
			parentID := findTemplateID(parent)
			trimmedparent := filepath.Base(parent)

			findParentTemplates(parentID, trimmedparent, ckmMirror /* ticketDir,  */, data)
		}
	}
	data.relationsetXML = append(data.relationsetXML, "</contained-in>")
	data.relationsetXML = append(data.relationsetXML, "<released-in>")

	for j := range versions {
		version := versions[j]
		if version == "" {
			continue
		}

		parts := strings.Split(version, ":")
		parent = parts[0]
		parent = strings.TrimSpace(parent)

		// remove the source template from the results.
		if strings.Contains(parent, file) {
			continue
		}
		/* 		if parent == file {
		   			continue
		   		}
		*/
		if parent != "" {
			log.Println("findParentTemplates version - " + parent)
			parentID := findTemplateID(parent)
			trimmedparent := filepath.Base(parent)

			findParentTemplates(parentID, trimmedparent, ckmMirror /* ticketDir, */, data)
		}

	}
	data.relationsetXML = append(data.relationsetXML, "</released-in>")

	data.relationsetXML = append(data.relationsetXML, "</template>")

	if len(results) > 1 {
		return true

	}
	return false
}

func grepDir(pattern string, ckmMirror string) string {
	cmd := exec.Command("grep", "-r", pattern, ckmMirror)
	var outbuf, errbuf bytes.Buffer
	cmd.Stdout = &outbuf
	cmd.Stderr = &errbuf
	err := cmd.Run()
	if err != nil {
		log.Printf("grepDir finished with error: %v", err)
	}
	stdout := outbuf.String()
	return stdout
}

func grepFile(file string, pattern string) string {

	cmd := exec.Command("grep", pattern, file)

	var outbuf, errbuf bytes.Buffer
	cmd.Stdout = &outbuf
	cmd.Stderr = &errbuf

	err := cmd.Run()
	if err != nil {
		log.Printf("grepFile finished with error: %v", err)
	}
	stdout := outbuf.String()
	return stdout
}

func printSession(data sessionData) string {

	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		fmt.Println("error:", err)
	}
	fmt.Print(string(b))

	return string(b)

}

func printMap(data sessionData) string {

	b, err := json.MarshalIndent(data.WuaNodes, "", "  ")
	if err != nil {
		fmt.Println("error:", err)
	}
	fmt.Print(string(b))

	return string(b)

}

// navigates through xml tree recursively and appends child->parent relationships to map
func mapTemplate(el *etree.Element, data *sessionData) bool {

	var slParents []*etree.Element
	var slVersions []*etree.Element
	var alreadyExists = false
	var foundrelation = -1
	var relation nodeDefinition

	if el == nil {
		return false
	}

	// TODO: multiple 'contained' templates
	eTemplateFilename := el.SelectElement("filename")
	eTemplateID := el.SelectElement("id")

	if (eTemplateFilename == nil) || (eTemplateID == nil) {
		return false
	}

	sCurrentTemplateFilename := eTemplateFilename.Text()
	sCurrentTemplateID := eTemplateID.Text()

	// add to unique list of known templates
	data.relationshipData[sCurrentTemplateFilename] = 0

	eContainedIn := el.SelectElement("contained-in")

	if eContainedIn != nil {
		slParents = eContainedIn.SelectElements("template")
	}

	// find the child
	for idx, r := range data.WuaNodes {
		if r.NodeID == sCurrentTemplateID {
			alreadyExists = true
			foundrelation = idx
			break
		}
	}

	if !alreadyExists { // create node if not found
		//relation.uid = ksuid.New().String()
		relation.NodeName = sCurrentTemplateFilename
		relation.NodeID = sCurrentTemplateID
		relation.NodeChanged = -1        // // not yet processed by precommit
		relation.NodeCommitOrder = -1    // not yet processed by precommit
		relation.NodeCommitIntended = -1 // not yet processed by precommit
		relation.NodeIsCommitted = -1    // not yet processed by commit
		relation.NodeValidated = -1      // not yet processed by precommit

		data.WuaNodes = append(data.WuaNodes, relation)

		foundrelation = len(data.WuaNodes) - 1
	} else {
		relation = data.WuaNodes[foundrelation]
	}

	for _, eParentTemplate := range slParents { // add all the parent relationships to the node
		if eParentTemplate != nil {
			var sParentFilename = ""
			var parentAlreadyMapped = false

			eParentFilename := eParentTemplate.SelectElement("filename")
			if eParentFilename != nil {
				sParentFilename = eParentFilename.Text()
			}
			if sParentFilename != "" {
				for _, parent := range data.WuaNodes[foundrelation].NodeParentList {
					if parent == sParentFilename {
						parentAlreadyMapped = true
						break
					}
				}
				if !parentAlreadyMapped {
					data.WuaNodes[foundrelation].NodeParentList = append(data.WuaNodes[foundrelation].NodeParentList, sParentFilename)
				}
				mapTemplate(eParentTemplate, data)
			}
		}
	}

	eReleasedIn := el.SelectElement("released-in")

	if eReleasedIn != nil {
		slVersions = eReleasedIn.SelectElements("template")
	}

	for _, eVersion := range slVersions { // add all the released version relationships to the node
		if eVersion != nil {
			var sVersionFilename = ""
			var versionAlreadyMapped = false

			eVersionFilename := eVersion.SelectElement("filename")
			if eVersionFilename != nil {
				sVersionFilename = eVersionFilename.Text()
			}
			if sVersionFilename != "" {
				for _, version := range data.WuaNodes[foundrelation].NodeReleasedList {
					if version == sVersionFilename {
						versionAlreadyMapped = true
						break
					}
				}
				if !versionAlreadyMapped {
					data.WuaNodes[foundrelation].NodeReleasedList = append(data.WuaNodes[foundrelation].NodeReleasedList, sVersionFilename)
				}
				mapTemplate(eVersion, data)
			}
		}
	}

	return true
}

func mapWhereUsedXML(ParentTree []string, data *sessionData) {
	// TODO: multiple root templates
	doc := etree.NewDocument()
	sXML := strings.Join(ParentTree, "\x20")
	sXML = strings.Replace(sXML, "&", "&amp;", 1)

	if err := doc.ReadFromString(sXML); err != nil {
		panic(err)
	}

	templates := doc.SelectElements("template")

	for _, template := range templates {
		if template != nil {
			mapTemplate(template, data)
		}
	}
}

// readLines reads a whole file into memory
// and returns a slice of its lines.
func readLines(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

func generateMap2(data sessionData, templatefile string) string {

	var generatedmap string
	template, err := readLines(templatefile)
	if err == nil {

		var line string
		var nodes string
		var edges string

		for s := range data.relationshipData {
			//{ data: { id: 'n0' } },
			nodes += "              { data: { id: '" + s + "' } }," + "\n"

		}
		for _, r := range data.WuaNodes {
			//              { data: { source: 'n0', target: 'n1' } },
			for _, p := range r.NodeParentList {
				var edges string
				edges += `              { data: { source: '` + r.NodeName + "', target: '" + p + "' } } ," + "\n"
			}

		}

		for i := range template {
			line = template[i]
			line = strings.Replace(line, "%%NODES%%", nodes, -1)
			line = strings.Replace(line, "%%EDGES%%", edges, -1)
			//fmt.Fprintln(w, line)
			generatedmap += line
		}
	}
	return generatedmap
}

func getLocalTemplateList(ticketPath string) []string {

	var files []string

	err := filepath.Walk(ticketPath, func(path string, info os.FileInfo, err error) error {

		if strings.ToLower(filepath.Ext(info.Name())) == ".oet" {

			if !strings.Contains(path, strings.ToLower("downloads")) {
				files = append(files, path)
			}
		}
		return nil
	})
	if err != nil {
		panic(err) // TODO remove panics

	}

	return files

}

func templateInSessionNodes(templateID string, data *sessionData) int {

	for i := 0; i < len(data.WuaNodes); i++ {
		if data.WuaNodes[i].NodeID == templateID {
			return i
		}
	}
	return -1

}

func addVersionToNode(node *nodeDefinition, sVersionFilename string, data *sessionData) bool {

	// find the node in the session list
	idx := templateInSessionNodes(node.NodeID, data)
	if idx > -1 {
		for _, version := range data.WuaNodes[idx].NodeReleasedList {
			if version == sVersionFilename {
				return true // version might be already recorded, can happen if matches are fonud locally and in mirror
			}
		}
		data.WuaNodes[idx].NodeReleasedList = append(data.WuaNodes[idx].NodeReleasedList, sVersionFilename)
		return true
	}
	return false
}

func addParentToNode(node *nodeDefinition, sParentFilename string, data *sessionData) bool {

	// find the node in the session list
	idx := templateInSessionNodes(node.NodeID, data)
	if idx > -1 {
		for _, parent := range data.WuaNodes[idx].NodeParentList {
			if parent == sParentFilename {
				return true // parent might be already recorded, can happen if matches are fonud locally and in mirror
			}
		}
		data.WuaNodes[idx].NodeParentList = append(data.WuaNodes[idx].NodeParentList, sParentFilename)
		return true
	}
	return false
}

func initNode(node *nodeDefinition, sCurrentTemplateFilename, sCurrentTemplateID string, data *sessionData) (isNew bool, idx int) {

	node.NodeName = sCurrentTemplateFilename
	node.NodeID = sCurrentTemplateID
	node.NodeChanged = -1        // // not yet processed by precommit
	node.NodeCommitOrder = -1    // not yet processed by precommit
	node.NodeCommitIntended = -1 // not yet processed by precommit
	node.NodeIsCommitted = -1    // not yet processed by commit
	node.NodeValidated = -1      // not yet processed by precommit

	idx = templateInSessionNodes(node.NodeID, data)

	if idx > -1 {
		// node already exists
		return false, idx
	}

	// if node is new, add it to session list
	data.WuaNodes = append(data.WuaNodes, *node)
	idx = len(data.WuaNodes) - 1

	return true, idx
}

func templateToNode(node *nodeDefinition, data *sessionData) bool {

	// pass in the node, find and add the parents to the node. For each parent, create a node and call templateToNode()

	ticketDir := "./" + data.ChangesetFolder + "/"

	// find parents
	log.Printf("templateToNode( " + node.NodeName + " / " + node.NodeID)

	var foundfiles = grepDir("template_id=\""+node.NodeID, data.sessionConfig.MirrorCkmPath)
	var foundlocalfiles = grepDir("template_id=\""+node.NodeID, ticketDir)
	results := strings.Split(foundlocalfiles+"\n"+foundfiles, "\n")

	var foundversions = grepDir("{AHSID~"+node.NodeID, data.sessionConfig.MirrorCkmPath) // TODO move traceability token to .config
	var foundlocalversions = grepDir("{AHSID~"+node.NodeID, ticketDir)
	versions := strings.Split(foundlocalversions+"\n"+foundversions, "\n")

	// add the parents to the node

	// add the versions to the node

	// for each parent,
	parent := ""

	for i := range results {
		result := results[i]
		parts := strings.Split(result, ":")
		parent = parts[0]
		parent = strings.TrimSpace(parent)

		if parent != "" {
			log.Println("templateToNode parent - " + parent)
			parentID := findTemplateID(parent)
			trimmedparent := filepath.Base(parent)

			//node.NodeParentList = append(node.NodeParentList, trimmedparent)
			addParentToNode(node, trimmedparent, data)

			// create a new node / relation
			var parentNode nodeDefinition
			isNew, _ := initNode(&parentNode, trimmedparent, parentID, data)
			if isNew {
				templateToNode(&parentNode, data)
			}
		}
	}
	// find released versions

	for j := range versions {
		version := versions[j]
		if version == "" {
			continue
		}

		parts := strings.Split(version, ":")
		parent = parts[0]
		parent = strings.TrimSpace(parent)

		// remove the source template from the results.
		if strings.Contains(parent, node.NodeName) {
			continue
		}

		if parent != "" {
			log.Println("templateToNode version - " + parent)
			parentID := findTemplateID(parent)
			trimmedparent := filepath.Base(parent)

			//node.NodeReleasedList = append(node.NodeReleasedList, trimmedparent)
			addVersionToNode(node, trimmedparent, data)

			// create a new node / relation
			var parentNode nodeDefinition
			isNew, _ := initNode(&parentNode, trimmedparent, parentID, data)
			if isNew {
				templateToNode(&parentNode, data)
			}

		}

	}
	return true

	///---------------------------------------------------------------
	/*
		if id == "" {
			log.Printf("findParentTemplates failure....no id passed in")
			return false
		}

		log.Printf("findParentTemplates( " + id + " / " + file)

		var foundfiles = grepDir("template_id=\""+id, ckmMirror)
		var foundlocalfiles = grepDir("template_id=\""+id, ticketDir)
		results := strings.Split(foundlocalfiles+"\n"+foundfiles, "\n")

		var foundversions = grepDir("{AHSID~"+id, ckmMirror) // TODO move traceability token to .config
		var foundlocalversions = grepDir("{AHSID~"+id, ticketDir)
		versions := strings.Split(foundlocalversions+"\n"+foundversions, "\n")

		data.relationsetXML = append(data.relationsetXML, "<template><filename>"+file+"</filename><id>"+id+"</id><contained-in>")

		parent := ""

		for i := range results {
			result := results[i]
			parts := strings.Split(result, ":")
			parent = parts[0]
			parent = strings.TrimSpace(parent)

			if parent != "" {
				log.Println("findParentTemplates parent - " + parent)
				parentID := findTemplateID(parent)
				trimmedparent := filepath.Base(parent)

				findParentTemplates(parentID, trimmedparent, ckmMirror, data)
			}
		}
		data.relationsetXML = append(data.relationsetXML, "</contained-in>")
		data.relationsetXML = append(data.relationsetXML, "<released-in>")

		for j := range versions {
			version := versions[j]
			if version == "" {
				continue
			}

			parts := strings.Split(version, ":")
			parent = parts[0]
			parent = strings.TrimSpace(parent)

			// remove the source template from the results.
			if strings.Contains(parent, file) {
				continue
			}

			if parent != "" {
				log.Println("findParentTemplates version - " + parent)
				parentID := findTemplateID(parent)
				trimmedparent := filepath.Base(parent)

				findParentTemplates(parentID, trimmedparent, ckmMirror , data)
			}

		}
		data.relationsetXML = append(data.relationsetXML, "</released-in>")

		data.relationsetXML = append(data.relationsetXML, "</template>")

		if len(results) > 1 {
			return true

		}
		return false */

}

func mapTicketTemplates2(mirrorPath string, data *sessionData) {

	var files []string
	files = getLocalTemplateList(data.ChangesetFolder)
	if files != nil {

		for _, file := range files {
			log.Printf("mapTicketTemplates2: " + file)
			updateSessionStatus("Mapping Asset : "+file, data)

			templateID := findTemplateID(file)

			// create node
			var node nodeDefinition
			trimmedfile := filepath.Base(file)
			isNew, _ := initNode(&node, trimmedfile, templateID, data)
			if isNew {
				templateToNode(&node, data)
			}
		}
	}

}

func mapTicketTemplates(mirrorPath string, data *sessionData) {

	var files []string
	files = getLocalTemplateList(data.ChangesetFolder)
	if files != nil {

		for _, file := range files {
			log.Printf("loadTicketTemplates: " + file)
			updateSessionStatus("Mapping Asset : "+file, data)

			templateID := findTemplateID(file)
			findParentTemplates(templateID, filepath.Base(file), mirrorPath /* ticketPath, */, data)
			mapWhereUsedXML(data.relationsetXML, data)
		}
	}

}

func getHashAndChangedStatus2(data *sessionData, quick bool) {
	var files []string

	files = getLocalTemplateList(data.ChangesetFolder)
	for _, file := range files { // for all the local files

		hash := hashTemplate(file) // generate the hash for the local file
		log.Println(file + " : " + hash)
		templatename := filepath.Base(file)

		updateSessionStatus("Processing status for asset : "+file, data)

		// store hash in where-used array
		for i := 0; i < len(data.WuaNodes); i++ {
			if data.WuaNodes[i].NodeName == templatename {
				data.WuaNodes[i].NodeHash = hash
				data.WuaNodes[i].NodeLocation = file

				ckmHash := ""
				templateexists := false
				cid := ""

				if !quick {

					templateexists, cid = cacheGetCidFromID(data.WuaNodes[i].NodeID, data)
				} else {
					templateexists = true
				}

				if templateexists {
					data.WuaNodes[i].NodeCID = cid

					ckmHash = cacheGetHash(data.WuaNodes[i], data)

					switch {
					case (ckmHash != data.WuaNodes[i].NodeHash):
						data.WuaNodes[i].NodeChanged = 1
					case (ckmHash == data.WuaNodes[i].NodeHash):
						data.WuaNodes[i].NodeChanged = 0
					default:
						data.WuaNodes[i].NodeChanged = -1
					}
				} else {
					data.WuaNodes[i].NodeChanged = 2 // template doesn't exist in CKM, see ckmCommitNewTemplate()
				}

			}
		}
	}
}

// returns md5 hash for file, using (linux) standard utility (md5sum)
func hashTemplate(file string) string {

	log.Printf("hashTemplate : " + file)
	cmd := exec.Command("md5sum", file)

	var outbuf, errbuf bytes.Buffer
	cmd.Stdout = &outbuf
	cmd.Stderr = &errbuf

	err := cmd.Run()
	if err != nil {
		log.Printf("hashTemplate finished with error: %v", err)
	}
	stdout := outbuf.String()

	hash := strings.Split(stdout, " ")[0]
	return hash

}

func findTemplateInMirror(node nodeDefinition, data *sessionData) (isFound bool, path string) {

	foundfiles := grepDir("<id>"+node.NodeID, data.sessionConfig.MirrorCkmPath)
	results := strings.Split(foundfiles, "\n")

	foundfile := ""

	for i := range results {
		result := results[i]
		parts := strings.Split(result, ":")
		foundfile = parts[0]
		foundfile = strings.TrimSpace(foundfile)
		if foundfile != "" {
			log.Println("findTemplateInMirror foundfile - " + foundfile)
			parentID := findTemplateID(foundfile)
			if parentID == node.NodeID {
				return true, foundfile
			}
		}
	}
	return false, ""
}

func cacheGetHash(node nodeDefinition, data *sessionData) string {

	// find file in mirror
	inMirror, path := findTemplateInMirror(node, data)

	if inMirror {
		return hashTemplate(path) // generate the hash for the local file
	} else {
		return ckmGetHash(node.NodeCID, data)
	}
}

func ckmGetHash(cid string, data *sessionData) string {
	if contentdata, err := ckmGetContentXML("https://ahsckm.ca/ckm/rest/v1/templates/"+cid+"/hash", data); err != nil {
		log.Printf("Failed to get XML: %v", err)
	} else {
		log.Println("Received XML:" + string(contentdata))
		return string(contentdata)
	}

	return ""
}

func ckmValidateTemplate(node *nodeDefinition, data *sessionData) bool {

	templatesource, err := readLines(node.NodeLocation)
	initialfail := false

	if err != nil {
		return false
	}

	body := strings.NewReader(strings.Join(templatesource, "\x20"))
	req, err := retryablehttp.NewRequest("POST", "https://ahsckm.ca/ckm/rest/v1/templates/validation-report", body)

	if err != nil {
		return false
	}

	//req.Header.Set("Authorization", "Basic "+token)
	//req.Header.Set("JSESSIONID", token)
	req.SetBasicAuth(data.authUser, data.authPassword)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/xml")

	client := retryablehttp.NewClient()
	client.CheckRetry = defaultRetryPolicy
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 { // not success
		log.Println("ERROR: validation-report response statuscode = " + strconv.FormatInt(int64(resp.StatusCode), 10))
		initialfail = true

	}

	// need to read the validation report....

	bodydata, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return false
	}

	var report validationReport
	err = json.Unmarshal(bodydata, &report)
	if err == nil {
		if len(report) > 0 {
			if report[0].ValidationSeverity != "" {
				log.Println("ERROR: validation-report returned a problem : " + report[0].ErrorText + ", " + report[0].ValidationSeverity)
				return false
			}
		}
	} else {
		return false
	}

	log.Println("validated : " + node.NodeLocation)

	return true && !initialfail
}

// commit a template revision to ckm [NOTE: see also ckmCommitRevisedTemplate() ]
func ckmCommitNewTemplate(node *nodeDefinition, data *sessionData) bool {

	logmessage := url.QueryEscape(data.ChangeDetail)
	//logmessage := html.EscapeString(data.ChangeDetail)

	templatesource, err := readLines(node.NodeLocation)
	if err != nil {
		setSessionFailure("ckmCommitNewTemplate: problem reading template "+err.Error(), data)
		return false
	}
	body := strings.NewReader(strings.Join(templatesource, "\x20"))

	templatetype := node.NodeType
	projectcid := node.NodeProjectCID

	theRequest := "https://ahsckm.ca/ckm/rest/v1/templates?template-type=" + templatetype + "&cid-project=" + projectcid + "&log-message=" + logmessage + "&proceed-if-outdated-resources-used=false"
	req, err := retryablehttp.NewRequest("POST", theRequest, body)

	if err != nil {
		setSessionFailure("ckmCommitNewTemplate: problem making POST request template "+err.Error(), data)
		return false
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/xml")
	//req.Header.Set("Authorization", "Basic "+data.ckmToken)
	//req.Header.Set("JSESSIONID", data.ckmToken)
	req.SetBasicAuth(data.authUser, data.authPassword)

	client := retryablehttp.NewClient()
	client.CheckRetry = defaultRetryPolicy

	resp, err := client.Do(req)
	if err != nil {
		setSessionFailure("ckmCommitNewTemplate: problem making POST request template "+err.Error(), data)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 { // not success

		setSessionFailure("ckmCommitNewTemplate: ERROR response statuscode = "+strconv.FormatInt(int64(resp.StatusCode), 10), data)
		log.Println(" theRequest = " + theRequest)
		log.Println(" theBody = " + strings.Join(templatesource, "\x20"))
		return false
	}

	// need to get the cid for the new template

	returneddata, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		setSessionFailure("ckmCommitNewTemplate: problem reading response "+err.Error(), data)
		return false
	}

	var report commitReport
	err = json.Unmarshal(returneddata, &report)
	if err == nil {
		if report.Cid == "" {
			setSessionFailure("ckmCommitNewTemplate: blank cid returned for committed template", data)
			return false
		}
	} else {
		setSessionFailure("ckmCommitNewTemplate: failed to get cid for committed template", data)
		return false
	}

	node.NodeCID = report.Cid
	log.Println("ckmCommitNewTemplate : " + node.NodeName)
	return true
}

func defaultRetryPolicy(ctx context.Context, resp *http.Response, err error) (bool, error) {
	// do not retry on context.Canceled or context.DeadlineExceeded
	if ctx.Err() != nil {
		return false, ctx.Err()
	}

	if err != nil {
		return true, err
	}
	// Check the response code. We retry on 500-range responses to allow
	// the server time to recover, as 500's are typically not permanent
	// errors and may relate to outages on the server side. This will catch
	// invalid response codes as well, like 0 and 999.
	if resp.StatusCode == 0 || (resp.StatusCode >= 405 && resp.StatusCode != 501) {
		return true, nil
	}

	return false, nil
}

// commit a template revision to ckm [NOTE: see also ckmCommitNewTemplate() ]

func ckmGetTemplateTemporarily(node *nodeDefinition, data *sessionData) (success bool, path string) {
	// get the file and write it to the working dir and return the path

	templateexists, cid := ckmGetCidFromID(node.NodeID, data) // check template exists in ckm
	path = data.ChangesetFolder + "/" + data.sessionConfig.WorkingFolderPath + "/" + node.NodeName
	if templateexists {
		node.NodeCID = cid
		err := ckmGetTemplateOET(*node, path)
		if err == nil {
			log.Println("ckmGetTemplateTemporarily: " + path)
			return true, path
		}
		{
			setSessionFailure("ckmGetTemplateTemporarily: "+err.Error(), data)
		}
	} else {
		setSessionFailure("ckmGetTemplateTemporarily: Can't find template in CKM : "+node.NodeName, data)
	}
	return false, ""
}

// TODO : capture new resource info returned from ckm (for the report...)
func ckmCommitRevisedTemplate(node *nodeDefinition, data *sessionData) bool {

	logmessage := url.QueryEscape(data.ChangeDetail)
	templatelocation := ""

	// if template is not local, we need to download it from ckm to reupload...
	if node.NodeChanged == -1 {
		success, path := ckmGetTemplateTemporarily(node, data)
		if success {
			templatelocation = path
		} else {
			return false
		}

	} else {
		templatelocation = node.NodeLocation
	}

	templatesource, err := readLines(templatelocation)
	if err != nil {
		return false
	}
	body := strings.NewReader(strings.Join(templatesource, "\x20"))

	req, err := retryablehttp.NewRequest("PUT", "https://ahsckm.ca/ckm/rest/v1/templates/"+node.NodeCID+"?log-message="+logmessage+"&proceed-if-outdated-resources-used=false", body)

	if err != nil {
		// handle err
		return false
	}
	req.Header.Set("Accept", "application/xml")
	req.Header.Set("Content-Type", "application/xml")
	//req.Header.Set("Authorization", "Basic "+data.ckmToken)
	//req.Header.Set("JSESSIONID", data.ckmToken)
	req.SetBasicAuth(data.authUser, data.authPassword)

	client := retryablehttp.NewClient()
	client.CheckRetry = defaultRetryPolicy

	resp, err := client.Do(req)

	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 { // not success
		//log.Println("ERROR: ckmCommitRevisedTemplate response statuscode = " + strconv.FormatInt(int64(resp.StatusCode), 10))
		//updateSessionStatus("ERROR: ckmCommitRevisedTemplate response statuscode = "+strconv.FormatInt(int64(resp.StatusCode), 10), data)
		setSessionFailure("ERROR: ckmCommitRevisedTemplate response statuscode = "+strconv.FormatInt(int64(resp.StatusCode), 10), data)
		return false
	}
	log.Println("ckmCommitRevisedTemplate : " + node.NodeName)
	// check local vs ckm
	// if ckm has later version, fail

	// Commit

	// 			// if it is a new revision
	// 			//   - checkout?
	// 			//   - upload

	// 			// if it is a brand new template
	// 			//  -

	// 			// -- anything else?

	return true
}

func processTreeTopFirst(relation *nodeDefinition, isTop bool, nodeOrderList *[]string, data *sessionData, dryrun bool) bool {

	*nodeOrderList = append(*nodeOrderList, relation.NodeName)
	bumpparent := false

	if relation.NodeChanged > 0 {
		relation.NodeCommitIntended = 1
		log.Println(relation.NodeName + " has changed, so we intent to commit it [1]")
		// validation moved to commit phase, as templates with new embedded templates cannot be validated.
		bumpparent = true
	}

	// navigate the tree top down
	// check no children not committed
	for i := 0; i < len(data.WuaNodes); i++ { // iterate all nodes
		for j := 0; j < len(data.WuaNodes[i].NodeParentList); j++ { // for a given node, look through its parents
			if data.WuaNodes[i].NodeParentList[j] == relation.NodeName { // is the relation a parent of this node?
				thechildnode := &data.WuaNodes[i] // the direct child of the relation

				if processTreeTopFirst(thechildnode, false, nodeOrderList, data, dryrun) {
					bumpparent = true
					// relation's decendents have been changed, so a version bump is needed
					log.Println(thechildnode.NodeName + " or its decendant has changed, so " + relation.NodeName + " needs a bump")

					if relation.NodeCommitIntended < 1 {
						// validation moved to commit phase, as templates with new embedded templates cannot be validated.
						relation.NodeCommitIntended = 2
					}
				}

			}
		}
	}
	// each node added to list

	// when all top nodes mapped, iterate list
	return bumpparent // return true if decendent has been changed.

}

func ckmGetTemplateOET(node nodeDefinition, targetfile string) error { // TODO check return code / 404 issue

	// Get the data
	client := retryablehttp.NewClient()
	client.CheckRetry = defaultRetryPolicy
	resp, err := client.Get("https://ahsckm.ca/ckm/rest/v1/templates/" + node.NodeCID + "/oet")
	if err != nil || resp.StatusCode != 200 {
		return err
	}
	defer resp.Body.Close()

	//out, err := os.Create(node.NodeLocation)
	out, err := os.Create(targetfile)
	if err != nil {
		return err
	}
	defer out.Close()

	// Write the body to file
	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return err
	}

	return nil

}

func sendWUVToBrowser(statusSessionID string) string {

	data := getSessionData(statusSessionID)
	var nodes string
	var edges string
	var nodecolor string

	files := getLocalTemplateList(data.ChangesetFolder)

	for s := range data.WuaNodes {

		found := false

		for _, file := range files {
			log.Println(file)
			if strings.Contains(file, data.WuaNodes[s].NodeName) {
				found = true
				break
			}
		}
		/*
			nodemodifier := "" */

		if found {
			nodecolor = "#4eadfc\""
		} else {
			nodecolor = "#dadee8\""
		}

		//if

		nodes += "{ \"data\": { \"id\": \"" + strings.Replace(data.WuaNodes[s].NodeName, ".oet", "", -1) + "\", \"bg\": \"" + nodecolor + " } },"
	}
	for _, r := range data.WuaNodes {
		r.NodeName = strings.Replace(r.NodeName, ".oet", "", -1)
		for _, p := range r.NodeParentList {
			p = strings.Replace(p, ".oet", "", -1)
			edges += "{ \"data\": { \"style\": \"solid\",  \"color\": \"rgba(239, 121, 45, 0.95)\",  \"arrowcolor\": \"rgba(239, 121, 45, 0.95)\", \"source\": \"" + r.NodeName + "\", \"target\": \"" + p + "\" } },"
		}
		for _, v := range r.NodeReleasedList {
			v = strings.Replace(v, ".oet", "", -1)
			edges += "{ \"data\": { \"style\": \"dashed\", \"color\": \"#b2b0a9\",  \"arrowcolor\": \"#b2b0a9\", \"source\": \"" + r.NodeName + "\", \"target\": \"" + v + "\" } },"
		}

	}

	log.Println("sendGraphDataToBrowser: " + "[ [" + nodes + "],[" + edges + "] ]")

	nodes = strings.TrimSuffix(nodes, ",")
	edges = strings.TrimSuffix(edges, ",")
	return ("[ [" + nodes + "],[" + edges + "] ]")
}

func sendReportToBrowser(statusSessionID string) string {
	data := getSessionData(statusSessionID)
	if data != nil {
		return printMap(*data)
	}
	return ""
}

func sendProjectsToBrowser() (status bool, projects string) {

	projectjson := ""

	req, err := retryablehttp.NewRequest("GET", "https://ahsckm.ca/ckm/rest/v1/projects?include-order-template-projects=true&show-all-to-admin=true", nil)

	if err != nil {
		return false, ""
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/xml")

	client := retryablehttp.NewClient()
	client.CheckRetry = defaultRetryPolicy
	resp, err := client.Do(req)
	if err != nil {
		return false, ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 { // not success
		log.Println("ERROR: ckm/rest/v1/projects response statuscode = " + strconv.FormatInt(int64(resp.StatusCode), 10))
		return false, ""
	}

	// need to read the PROJECT report....

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return false, ""
	}

	projectjson = string(data)

	return true, projectjson
}

func sendStatusToBrowser(statusSessionID string) string {
	data := getSessionData(statusSessionID)
	var objStatus = new(status)

	if data != nil {

		objStatus.Message = data.StatusText
		objStatus.ProcessingStage = data.ProcessingStage
		objStatus.IsError = data.IsError

		b, err := json.Marshal(objStatus)
		if err != nil {
			fmt.Println("error:", err)
		}

		result := string(b)
		return result

	}
	return ""
}

// Contains tells whether a contains x.
func Contains(a []string, x string) bool {
	for _, n := range a {
		if x == n {
			return true
		}
	}
	return false
}

func backupTicket(data sessionData) string {

	now := time.Now()
	secs := now.Unix()
	zipfile := data.ChangesetFolder + "/" + data.sessionConfig.WorkingFolderPath + "/ticketsnapshot" + strconv.FormatInt(int64(secs), 10) + ".zip"

	cmd := exec.Command("zip", "-r", zipfile, data.ChangesetFolder, "--exclude", "*.zip")
	var outbuf, errbuf bytes.Buffer
	cmd.Stdout = &outbuf
	cmd.Stderr = &errbuf

	err := cmd.Run()
	if err != nil {
		log.Printf("BackupTicket finished with error: %v", err)
	}

	stdout := outbuf.String()
	return stdout
}