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

	"github.com/Tkanos/gonfig"
	"github.com/beevik/etree"
	"github.com/hashicorp/go-retryablehttp"
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
	NodeIsLocal        int      // [0,1] - ckm/mirror, local
	NodeRootEdited     int      // [0,1] - no, yes
	NodeRootNewText    string   // if root node edited, this will contain the updated text
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

	authUser       string
	authPassword   string
	sessionlogfile *os.File // per-session logging target
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

func loggingEnd(data *sessionData) {

	var f = data.sessionlogfile
	defer f.Close()
}

func loggingInit(data *sessionData) {

	f, err := os.OpenFile(data.ChangesetFolder+"/"+data.sessionConfig.WorkingFolderPath+"/"+data.sessionID+".log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening file: %v", err)
	}
	data.sessionlogfile = f
}

func loggingWrite(data *sessionData, message string) {

	log.Println(message)
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	var f = data.sessionlogfile
	w := bufio.NewWriter(f)

	w.WriteString(timestamp + ": " + message + "\n\n")
	w.Flush()

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
		loggingWrite(thisSessionData, "-----> starting 'precommit' process....")
		//backup(thisSessionData.ChangesetFolder, WorkingFolderPath, )
		backupTicket(thisSessionData)

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
		loggingWrite(thisSessionData, "-----> starting 'commit' process....")
		// TODO: enable client edit of change message

		/* 		if len(params) > 3 {
		   			committext := params[3]
		   			thisSessionData.ChangeDetail = committext
		   		}
		*/
		thisSessionData.ProcessingStage = 3 // started commit
		go commitProcessing(thisSessionData.ChangesetFolder, thisSessionData.sessionConfig.MirrorCkmPath, thisSessionData)
		fmt.Fprintln(w, sendStatusToBrowser(statusSessionID))
		//loggingEnd(thisSessionData)
		return
	}

	if param0 == "projects" {
		//fmt.Fprintln(w, sendStatusToBrowser(statusSessionID))
		statusSessionID := params[1] // sessionID
		thisSessionData := getSessionData(statusSessionID)
		status, projects := sendProjectsToBrowser(thisSessionData)
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
				getHashAndChangedStatus3(thisSessionData, true)

				walktree(thisSessionData)
				//printMap(*thisSessionData)
				//fmt.Fprintln(w, sendWUVToBrowser(thisSessionData.sessionID))
				fmt.Fprintln(w, sendReportToBrowser(thisSessionData.sessionID))
				loggingEnd(thisSessionData)
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
		panic(err) // TODO:
	}
	thisSessionData.WuaNodes = make([]nodeDefinition, 0)
	thisSessionData.relationshipData = make(map[string]int) // where-used map
	//thisSessionData.sessionID = ksuid.New().String()
	thisSessionData.sessionID = strconv.FormatInt(int64(time.Now().Unix()), 10)
	thisSessionData.ProcessingStage = 0
	thisSessionData.IsError = false
	thisSessionData.relationsetXML = []string{} // used to store the relationships between files
	thisSessionData.nodeOrderList = []string{}  // used to store the order of the commit (filename)
	gSessionDataList = append(gSessionDataList, &thisSessionData)

	//loggingWrite(&thisSessionData, "ckmpath = " + (string)(thisSessionData.sessionConfig.MirrorCkmPath))

	switch {

	case param0 == "init":
		changesetFolder = thisSessionData.sessionConfig.ChangesetPath + "/" + params[1] // ticket'
		thisSessionData.ChangesetFolder = changesetFolder
		if checkEnvironment(thisSessionData) == false {
			setSessionFailure("Exiting due to environment/config issues...", &thisSessionData)
			return
		}

		loggingInit(&thisSessionData)

		template, err := readLines(thisSessionData.sessionConfig.HTMLStatusTemplate)
		loggingWrite(&thisSessionData, "------> starting 'init' preprocess....")

		if len(params) < 5 {
			setSessionFailure("Exiting due to lack of parameters passed in (check change detail * auth token)...", &thisSessionData)
			return

		}

		if err == nil {
			if len(params) > 2 {
				str := params[2]
				//loggingWrite(&thisSessionData, "base64 change detail = "+thisSessionData.ChangeDetail)
				data, err := base64.StdEncoding.DecodeString(str)
				if err != nil {
					loggingWrite(&thisSessionData, "error:"+err.Error())
					return
				}

				thisSessionData.ChangeDetail = string(data)
				loggingWrite(&thisSessionData, "change detail = "+thisSessionData.ChangeDetail)
			}

			if len(params) > 4 {
				sUser := params[3] // user
				sPW := params[4]   // password

				data, err := base64.StdEncoding.DecodeString(sPW)
				if err != nil {
					loggingWrite(&thisSessionData, "pw decode error:"+err.Error())
					return
				}

				thisSessionData.authPassword = string(data)

				data, err = base64.StdEncoding.DecodeString(sUser)
				if err != nil {

					loggingWrite(&thisSessionData, "error:"+err.Error())
					return
				}
				loggingWrite(&thisSessionData, "user = "+string(data))
				thisSessionData.authUser = string(data)

				/* 				thisSessionData.ckmToken = str
				   				loggingWrite(data, "ckm token = " + thisSessionData.ckmToken)
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
		/* 		thisSessionData.ChangesetFolder = thisSessionData.sessionConfig.ChangesetPath + "/" + params[1]
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
		   				loggingWrite(data, file)
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

		   					loggingWrite(data, "id: " + templateid + " cid: " + cid)
		   					// get template filepack url

		   					filepack := ckmGetTemplateFilepackURL(cid, &thisSessionData)
		   					loggingWrite(data, "filepack = " + filepack)
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
		   					loggingWrite(data, "parseParentsTree : template doesn't exist : " + templateid)
		   				}
		   			}

		   		}
		   		status := ""

		   		status = moveFiles(thisSessionData.ChangesetFolder, "templates", thisSessionData.sessionConfig.WorkingFolderPath)
		   		loggingWrite(data, status)
		   		fmt.Fprintf(w, "<h3>grabbed"+status+"</h3>")

		   		status = moveFiles(thisSessionData.ChangesetFolder, "archetypes", thisSessionData.sessionConfig.WorkingFolderPath)
		   		loggingWrite(data, status)
		   		fmt.Fprintf(w, "<h3>grabbed"+status+"</h3>") */

		// before downloading, back it up

	case param0 == "get-map":

		thisSessionData.ChangesetFolder = thisSessionData.sessionConfig.ChangesetPath + "/" + params[1]

		if checkEnvironment(thisSessionData) == false {
			setSessionFailure("Exiting due to environment/config issues...", &thisSessionData)
			return
		}

		loggingInit(&thisSessionData)
		loggingWrite(&thisSessionData, "starting 'get-map' process....")
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
		log.Println("unknown operation type: " + param0)
		log.Println("exiting...")
		return
	}

}
func setSessionFailure(status string, data *sessionData) {
	loggingWrite(data, "---------------------------> setSessionFailure : "+status)
	data.StatusText = status
	data.IsError = true
}
func updateSessionStatus(status string, data *sessionData) {
	loggingWrite(data, status)

	data.StatusText = status
}

func getNodeTypeAndProject(node nodeDefinition, metadata string, data *sessionData) (status bool, assettype, project string) {
	status = false

	splits := strings.Split(strings.ToLower(metadata), "%5e")
	if len(splits) > 1 {

		for _, def := range splits {
			nodedef := strings.Split(def, "~")
			loggingWrite(data, "found : "+nodedef[0])
			if nodedef[0] == node.NodeID {
				assettype = nodedef[1]
				project = nodedef[2]
				status = true
			}
		}
	}

	assettype = strings.ToUpper(strings.Replace(assettype, "%20", "_", 1))

	loggingWrite(data, "asset type = "+assettype)
	loggingWrite(data, "project cid = "+project)

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
				updateSessionStatus("Working to commit this: "+node.NodeName, data)

				insertTraceability(&node, data)
				insertUpdatedRootNodes(&node, data)

				if node.NodeValidated < 0 {
					if node.NodeIsLocal == 1 { // don't bother validating as there's no local copy. We'll get the asset down from CKM and (re)upload in the commit.
						if !ckmValidateTemplate(&data.WuaNodes[idx], data) {
							data.WuaNodes[idx].NodeValidated = 0
							setSessionFailure("commitProcessing : ERROR validate template failed for "+data.WuaNodes[idx].NodeName, data)
							problems = true
							break
						}
						data.WuaNodes[idx].NodeValidated = 1
					}
				}

				if node.NodeChanged == 2 {
					// new ndoe
					found, assettype, cid := getNodeTypeAndProject(node, data.NewAssetMetadata, data)
					problems = !found

					if !problems {
						data.WuaNodes[idx].NodeType = assettype
						data.WuaNodes[idx].NodeProjectCID = cid

						if !ckmCommitNewTemplate(&data.WuaNodes[idx], data) {
							problems = true
						}
						loggingWrite(data, "New: "+node.NodeName)
					}
				} else {
					// existing node
					if !ckmCommitRevisedTemplate(&data.WuaNodes[idx], data) {
						problems = true
					}
					loggingWrite(data, "update: "+node.NodeName)
				}

				if !problems {

					if node.NodeIsLocal > 0 { // grab a fresh copy from CKM to replace the local one (which was just committed)
						// this is done because local copies can have different linewrapping (unix vs windows), which causes hash differences between the "same"
						// versions in ckm vs local.
						err := ckmGetTemplateOET(node, node.NodeLocation, data)
						if err != nil {
							setSessionFailure("ERROR: failed to get copy of committed template from CKM", data)
							problems = true
							return problems
						}
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
	loggingEnd(data)
	return problems
}

func precommitProcessing( /* changesetFolder string, */ mirrorpath string, data *sessionData) {

	updateSessionStatus("Building template maps", data)

	mapTicketTemplates2( /* "./"+data.ChangesetFolder+"/", */ mirrorpath, data)

	updateSessionStatus("Getting template metadata from CKM (hashes, cids, etc)", data)

	getHashAndChangedStatus3(data, false)

	walktree(data)
	setCommitOrder(data)
	loggingWrite(data, "*** Final node order list ***")
	loggingWrite(data, strings.Join(data.nodeOrderList, " "))
	loggingWrite(data, "*** ********************* ***")
	data.ProcessingStage = 2
	return
}

func walktree(data *sessionData) {

	// walk down tree, starting at each head node
	for i := 0; i < len(data.WuaNodes); i++ {
		if len(data.WuaNodes[i].NodeParentList) == 0 {

			treeorder := []string{}

			loggingWrite(data, "precommitProcessing: processTreeTopFirst( "+data.WuaNodes[i].NodeName+")")
			if processTreeTopFirst(&data.WuaNodes[i], true, &treeorder, data, true) { // dry run flag set
				if data.WuaNodes[i].NodeCommitIntended < 1 {
					data.WuaNodes[i].NodeCommitIntended = 2
				}
			}
			mergeTraverseList(treeorder, data)
			loggingWrite(data, "*** node order list after "+data.WuaNodes[i].NodeName+" ***")
			loggingWrite(data, strings.Join(data.nodeOrderList, " "))
			loggingWrite(data, "*** ********************* ***")
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
					data.WuaNodes[idx].NodeCommitOrder = order
					order++
				}
			}
		}
	}
}

// mergeTraverseList() takes a an ordered list of node names and merges into the session data masterlist
// of nodes to commit, keeping the same relative order
func mergeTraverseList(treeorder []string, data *sessionData) {

	loggingWrite(data, "mergeTraverseList: list to merge = ")
	loggingWrite(data, strings.Join(treeorder, " "))

	// merge tree order into session commit order
	last := len(treeorder) - 1
	for i := range treeorder {

		node := treeorder[last-i]
		// for each node, check if it exists in the masterlist
		nodeExists := false
		for _, name := range data.nodeOrderList {
			if name == node {
				nodeExists = true
			}
		}

		// if it doesnt exist, append it
		if !nodeExists {
			data.nodeOrderList = append(data.nodeOrderList, node) // .... (update: seems to work ok?)
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

func moveFiles(changesetFolder, assetType, WorkingFolderPath string, data *sessionData) string {

	cmd := exec.Command("rsync", "-av", "--ignore-existing", "--remove-source-files", changesetFolder+"/"+WorkingFolderPath+"/unzipped/"+assetType, changesetFolder)
	var outbuf, errbuf bytes.Buffer
	cmd.Stdout = &outbuf
	cmd.Stderr = &errbuf

	err := cmd.Run()
	if err != nil {
		loggingWrite(data, "moveFiles finished with error: "+err.Error())
	}

	stdout := outbuf.String()
	return stdout
}

func ckmGetTemplateFilepackURL(cid string, data *sessionData) (filesetURL string) {

	if contentdata, err := ckmGetContentPlain("https://ahsckm.ca/ckm/rest/v1/templates/"+cid+"/file-set-url", data); err != nil {
		loggingWrite(data, "Failed to get XML: "+err.Error())
	} else {
		//check(err)
		loggingWrite(data, "Received XML:"+string(contentdata))
		return string(contentdata)
	}
	return ""
}

func cacheGetCidFromID(id string, data *sessionData) (status bool, cid string) {

	// TODO: implement local cache
	//	return true, "fake"
	status, cid = ckmGetCidFromID(id, data)
	loggingWrite(data, "ckmGetCidFromID("+id+") = "+cid)
	return status, cid
}

func ckmGetCidFromID(id string, data *sessionData) (status bool, cid string) {

	req, err := retryablehttp.NewRequest("GET", "https://ahsckm.ca/ckm/rest/v1/templates/citeable-identifier/"+id, nil)
	if err != nil {
		setSessionFailure("ckmGetCidFromID :retryablehttp.NewRequest()", data)
		return false, ""
	}
	req.Header.Set("Accept", "text/plain")
	//req.SetBasicAuth(data.authUser, data.authPassword)

	client := retryablehttp.NewClient()
	client.CheckRetry = defaultRetryPolicy
	resp, err := client.Do(req)
	if err != nil {
		setSessionFailure("ckmGetCidFromID :client.Do(req)", data)
		return false, ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 || resp.StatusCode == 404 { // not success
		if resp.StatusCode != 404 {
			//loggingWrite(data, "ERROR: ckmGetCidFromID response statuscode = " + strconv.FormatInt(int64(resp.StatusCode), 10))
			setSessionFailure("ERROR: ckmGetCidFromID response statuscode = "+strconv.FormatInt(int64(resp.StatusCode), 10), data)
		}
		return false, ""
	}

	bodydata, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		setSessionFailure("ckmGetCidFromID : ioutil.ReadAll"+err.Error(), data)
		return false, ""
	}

	return true, string(bodydata)
}

func backup(changesetFolder, WorkingFolderPath string, relation nodeDefinition, data *sessionData) string {

	cmd := exec.Command("rsync", "-av", "--ignore-existing", "--remove-source-files", changesetFolder+"/"+WorkingFolderPath+"/backup/", relation.NodeLocation)
	var outbuf, errbuf bytes.Buffer
	cmd.Stdout = &outbuf
	cmd.Stderr = &errbuf

	err := cmd.Run()
	if err != nil {
		loggingWrite(data, "backup finished with error: "+err.Error())
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
	// TODO: check statuscode and implement setsessionfail
	defer resp.Body.Close()
	bodydata, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ckmGetContentXML: Read body: %v", err)
	}
	return bodydata, nil
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

func findTemplateID(path string, data *sessionData) string {
	// return the unique identifier for the template specified in the path param

	var templateID string
	lines, err := readLines(path)
	if err != nil {
		loggingWrite(data, "ERROR findTemplateID : "+err.Error())
		return templateID

	}
	content := strings.Join(lines, " ")
	splits := strings.Split(strings.ToLower(content), "<id>")
	if len(splits) > 1 {
		templateID = (splits[1])[0:36] // HACK: assumes id format is fixed....
	}

	return templateID

}

// find names of templates that contain id
func findParentTemplates(id string, file string, ckmMirror string /* ticketDir string,  */, data *sessionData) bool {

	ticketDir := "./" + data.ChangesetFolder + "/"

	if id == "" {
		loggingWrite(data, "findParentTemplates failure....no id passed in")
		return false
	}

	loggingWrite(data, "findParentTemplates( "+id+" / "+file)

	var foundfiles = grepDir("template_id=\""+id, ckmMirror)
	var foundlocalfiles = grepDir("template_id=\""+id, ticketDir)
	results := strings.Split(foundlocalfiles+"\n"+foundfiles, "\n")

	var foundversions = grepDir("{~AHSID~"+id, ckmMirror) // TODO:: move traceability token to .config
	var foundlocalversions = grepDir("{~AHSID~"+id, ticketDir)
	versions := strings.Split(foundlocalversions+"\n"+foundversions, "\n")

	data.relationsetXML = append(data.relationsetXML, "<template><filename>"+file+"</filename><id>"+id+"</id><contained-in>")

	parent := ""

	for i := range results {
		result := results[i]
		parts := strings.Split(result, ":")
		parent = parts[0]
		parent = strings.TrimSpace(parent)

		if parent != "" {
			loggingWrite(data, "findParentTemplates parent - "+parent)
			parentID := findTemplateID(parent, data)
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

		if parent != "" {
			loggingWrite(data, "findParentTemplates version - "+parent)
			parentID := findTemplateID(parent, data)
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

func grepDir(pattern string, dir string) string {
	//cmd := exec.Command("grep", "-r --exclude-dir=\"downloads\"", pattern, dir)
	cmd := exec.Command("grep", "-r", "--exclude-dir=downloads", pattern, dir)
	// grep -R menu ./ -i --exclude-dir="downloads"
	var outbuf, errbuf bytes.Buffer
	cmd.Stdout = &outbuf
	cmd.Stderr = &errbuf
	cmd.Run()

	stdout := outbuf.String()
	return stdout
}

func grepFile(file string, pattern string, data *sessionData) string {

	cmd := exec.Command("grep", pattern, file)

	var outbuf, errbuf bytes.Buffer
	cmd.Stdout = &outbuf
	cmd.Stderr = &errbuf

	err := cmd.Run()
	if err != nil {
		loggingWrite(data, "grepFile finished with error: "+err.Error())
	}
	stdout := outbuf.String()
	return stdout
}

func printSession(data sessionData) string {

	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		loggingWrite(&data, "printSession error:"+err.Error())
	}
	loggingWrite(&data, string(b))

	return string(b)

}

func printMap(data sessionData) string {

	b, err := json.MarshalIndent(data.WuaNodes, "", "  ")
	if err != nil {

		loggingWrite(&data, "printMap error:"+err.Error())
	}
	loggingWrite(&data, string(b))

	return string(b)

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

func getLocalTemplateList(ticketPath string, data *sessionData) []string {

	var files []string

	if _, err := os.Stat(ticketPath); os.IsNotExist(err) {
		setSessionFailure("getLocalTemplateList: "+ticketPath+" does not exist!", data)
		return nil
	}

	err := filepath.Walk(ticketPath, func(path string, info os.FileInfo, err error) error {

		if strings.ToLower(filepath.Ext(info.Name())) == ".oet" {

			if !strings.Contains(path, strings.ToLower("downloads")) {
				files = append(files, path)
			}
		}
		return nil
	})
	if err != nil {
		panic(err) // TODO: remove panics

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

func initNode(node *nodeDefinition, sCurrentTemplateFilename, sCurrentTemplateID, sCurrentFilePath string, nLocal int, data *sessionData) (isNew bool) {

	node.NodeName = sCurrentTemplateFilename
	node.NodeID = sCurrentTemplateID
	node.NodeLocation = sCurrentFilePath
	node.NodeChanged = -1        // // not yet processed by precommit
	node.NodeCommitOrder = -1    // not yet processed by precommit
	node.NodeCommitIntended = -1 // not yet processed by precommit
	node.NodeIsCommitted = -1    // not yet processed by commit
	node.NodeValidated = -1      // not yet processed by precommit
	node.NodeIsLocal = nLocal

	idx := templateInSessionNodes(node.NodeID, data)

	if idx > -1 {
		// node already exists

		// local copies should trump mirror copies that are already in the list
		if data.WuaNodes[idx].NodeLocation != node.NodeLocation {
			if strings.Contains(data.WuaNodes[idx].NodeLocation, data.sessionConfig.MirrorCkmPath) {
				// this new node should replace the mirror copy.
				data.WuaNodes[idx].NodeLocation = sCurrentFilePath
				data.WuaNodes[idx].NodeID = sCurrentTemplateID
				data.WuaNodes[idx].NodeID = sCurrentTemplateID
				data.WuaNodes[idx].NodeChanged = -1        // // not yet processed by precommit
				data.WuaNodes[idx].NodeCommitOrder = -1    // not yet processed by precommit
				data.WuaNodes[idx].NodeCommitIntended = -1 // not yet processed by precommit
				data.WuaNodes[idx].NodeIsCommitted = -1    // not yet processed by commit
				data.WuaNodes[idx].NodeValidated = -1      // not yet processed by precommit

				data.WuaNodes[idx].NodeIsLocal = nLocal

				return true
			}
		}

		return false
	}

	// if node is new, add it to session list
	data.WuaNodes = append(data.WuaNodes, *node)
	idx = len(data.WuaNodes) - 1

	return true
}

func templateToNode(node *nodeDefinition, data *sessionData) bool {

	// pass in the node, find and add the parents to the node. For each parent, create a node and call templateToNode()

	ticketDir := data.ChangesetFolder + "/"

	// find parents
	loggingWrite(data, "templateToNode( "+node.NodeName+" / "+node.NodeID)

	var foundfiles = grepDir("template_id=\""+node.NodeID, data.sessionConfig.MirrorCkmPath)
	var foundlocalfiles = grepDir("template_id=\""+node.NodeID, ticketDir)
	results := strings.Split(foundlocalfiles+"\n"+foundfiles, "\n")

	var foundversions = grepDir("{~AHSID~"+node.NodeID, data.sessionConfig.MirrorCkmPath) // TODO: move traceability token to .config
	var foundlocalversions = grepDir("{~AHSID~"+node.NodeID, ticketDir)
	versions := strings.Split(foundlocalversions+"\n"+foundversions, "\n")

	// add the parents to the node

	// add the versions to the node

	// for each parent,
	parent := ""

	for i := range results {
		result := results[i]
		parts := strings.Split(result, ":")
		parent = parts[0]
		nLocal := 1

		if strings.Contains(parent, data.sessionConfig.MirrorCkmPath) {
			nLocal = 0 // if it is in the ckm mirror, it's not local
		}

		parent = strings.TrimSpace(parent)

		if parent != "" {
			//loggingWrite(data, "templateToNode parent - " + parent)
			parentID := findTemplateID(parent, data)
			trimmedparent := filepath.Base(parent)

			//node.NodeParentList = append(node.NodeParentList, trimmedparent)

			if node.NodeName == trimmedparent {
				setSessionFailure("templateToNode(): Detected circular relationship in template structure (node = "+node.NodeName+", parent = "+trimmedparent, data)
				return false
			}

			addParentToNode(node, trimmedparent, data)

			// create a new node / relation
			var parentNode nodeDefinition
			isNew := initNode(&parentNode, trimmedparent, parentID, parent, nLocal, data)
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

		nLocal := 1

		if strings.Contains(parent, data.sessionConfig.MirrorCkmPath) {
			nLocal = 0
		}

		if parent != "" {
			loggingWrite(data, "templateToNode version - "+parent)
			parentID := findTemplateID(parent, data)
			trimmedparent := filepath.Base(parent)

			//node.NodeReleasedList = append(node.NodeReleasedList, trimmedparent)
			addVersionToNode(node, trimmedparent, data)

			// create a new node / relation
			var parentNode nodeDefinition
			isNew := initNode(&parentNode, trimmedparent, parentID, parent, nLocal, data)
			if isNew {
				templateToNode(&parentNode, data)
			}

		}

	}
	return true
}

func mapTicketTemplates2(mirrorPath string, data *sessionData) {

	var files []string
	files = getLocalTemplateList(data.ChangesetFolder, data)
	if files != nil {
		nLocal := 1 // all local files

		for _, file := range files {
			loggingWrite(data, "mapTicketTemplates2: "+file)
			updateSessionStatus("Mapping Asset : "+file, data)

			templateID := findTemplateID(file, data)

			// create node
			var node nodeDefinition
			trimmedfile := filepath.Base(file)
			isNew := initNode(&node, trimmedfile, templateID, file, nLocal, data)
			if isNew {
				templateToNode(&node, data)
			}
		}
	}

}
func getHashAndChangedStatus3(data *sessionData, quick bool) {

	for i := 0; i < len(data.WuaNodes); i++ {

		if quick {
			updateSessionStatus("Quickly processing status for asset : "+data.WuaNodes[i].NodeName, data)
		} else {
			updateSessionStatus("Processing status for asset : "+data.WuaNodes[i].NodeName, data)
		}

		// if file is local
		//if data.WuaNodes[i].NodeLocation != "" {
		if data.WuaNodes[i].NodeIsLocal > 0 {
			hash := hashTemplate(data.WuaNodes[i].NodeLocation, data) // generate the hash for the local file
			loggingWrite(data, data.WuaNodes[i].NodeName+" : "+hash)
			data.WuaNodes[i].NodeHash = hash
		}

		ckmHash := ""
		templateExistsInCKM := false
		cid := ""

		if !quick {

			templateExistsInCKM, cid = cacheGetCidFromID(data.WuaNodes[i].NodeID, data)
		} else {
			templateExistsInCKM = true
		}

		if templateExistsInCKM {
			data.WuaNodes[i].NodeCID = cid

			if data.WuaNodes[i].NodeIsLocal > 0 {
				ckmHash = cacheGetHash(data.WuaNodes[i], data)

				switch {
				case (ckmHash != data.WuaNodes[i].NodeHash):
					data.WuaNodes[i].NodeChanged = 1
					data.WuaNodes[i].NodeRootEdited, data.WuaNodes[i].NodeRootNewText = hasRootNodeBeenChanged(data.WuaNodes[i], data)
				case (ckmHash == data.WuaNodes[i].NodeHash):
					data.WuaNodes[i].NodeChanged = 0
				default:
					data.WuaNodes[i].NodeChanged = -1
				}

			}
		} else {
			data.WuaNodes[i].NodeChanged = 2 // template doesn't exist in CKM, see ckmCommitNewTemplate()
		}

	}

}

// returns md5 hash for file, using (linux) standard utility (md5sum)
func hashTemplate(file string, data *sessionData) string {

	loggingWrite(data, "hashTemplate : "+file)
	cmd := exec.Command("md5sum", file)

	var outbuf, errbuf bytes.Buffer
	cmd.Stdout = &outbuf
	cmd.Stderr = &errbuf

	err := cmd.Run()
	if err != nil {
		loggingWrite(data, "hashTemplate finished with error: "+err.Error())
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
			loggingWrite(data, "findTemplateInMirror foundfile - "+foundfile)
			parentID := findTemplateID(foundfile, data)
			if parentID == node.NodeID {
				return true, foundfile
			}
		}
	}
	return false, ""
}

// get the hash of the file that is in CKM (or the CKM Mirror)
func cacheGetHash(node nodeDefinition, data *sessionData) string {

	// find file in mirror
	inMirror, path := findTemplateInMirror(node, data)

	if inMirror {
		return hashTemplate(path, data) // generate the hash for the local mirror file
	}

	return ckmGetHash(node.NodeCID, data)

}

func ckmGetHash(cid string, data *sessionData) string {
	if contentdata, err := ckmGetContentXML("https://ahsckm.ca/ckm/rest/v1/templates/"+cid+"/hash", data); err != nil {
		loggingWrite(data, "Failed to get XML: "+err.Error())
	} else {
		loggingWrite(data, "Received XML:"+string(contentdata))
		return string(contentdata)
	}

	return ""
}

func ckmValidateTemplate(node *nodeDefinition, data *sessionData) bool {

	templatesource, err := readLines(node.NodeLocation)
	initialfail := false

	if err != nil {
		loggingWrite(data, "ckmValidateTemplate: couldn't readLines for node :"+node.NodeName)
		return false
	}

	body := strings.NewReader(strings.Join(templatesource, "\x20"))
	req, err := retryablehttp.NewRequest("POST", "https://ahsckm.ca/ckm/rest/v1/templates/validation-report", body)

	if err != nil {
		loggingWrite(data, "ckmValidateTemplate: NewRequest() failed :"+node.NodeName)
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
		loggingWrite(data, "ckmValidateTemplate: client.Do(req) failed :"+node.NodeName)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 { // not success
		loggingWrite(data, "ERROR: validation-report response statuscode = "+strconv.FormatInt(int64(resp.StatusCode), 10))
		initialfail = true

	}

	// need to read the validation report....

	bodydata, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		loggingWrite(data, "ckmValidateTemplate:  ioutil.ReadAll(resp.Body) failed :"+node.NodeName)
		return false
	}

	var report validationReport
	err = json.Unmarshal(bodydata, &report)
	if err == nil {
		if len(report) > 0 {
			if report[0].ValidationSeverity != "" {
				loggingWrite(data, "ERROR: validation-report returned a problem : "+report[0].ErrorText+", "+report[0].ValidationSeverity)
				return false
			}
		}
	} else {
		loggingWrite(data, "ckmValidateTemplate: json.Unmarshal(bodydata, &report) failed :"+node.NodeName)
		return false
	}

	loggingWrite(data, "validated : "+node.NodeLocation)

	return true && !initialfail
}

// commit a template revision to ckm [NOTE: see also ckmCommitRevisedTemplate() ]
func ckmCommitNewTemplate(node *nodeDefinition, data *sessionData) bool {

	logmessage := url.QueryEscape(data.ChangeDetail)

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
		loggingWrite(data, " theRequest = "+theRequest)
		loggingWrite(data, " theBody = "+strings.Join(templatesource, "\x20"))
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
	loggingWrite(data, "ckmCommitNewTemplate : "+node.NodeName)
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
		err := ckmGetTemplateOET(*node, path, data)
		if err == nil {
			loggingWrite(data, "ckmGetTemplateTemporarily: "+path)
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

// TODO: : capture new resource info returned from ckm (for the report...)
func ckmCommitRevisedTemplate(node *nodeDefinition, data *sessionData) bool {

	logmessage := url.QueryEscape(data.ChangeDetail)
	templatelocation := ""
	if node.NodeCID == "" {
		setSessionFailure("ckmCommitRevisedTemplate no NodeCID set on "+node.NodeName, data)
		return false
	}

	// if template is not local, we need to download it from ckm to reupload...
	//if node.NodeChanged == -1 {
	if node.NodeIsLocal < 0 {
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
	req.SetBasicAuth(data.authUser, data.authPassword)

	client := retryablehttp.NewClient()
	client.CheckRetry = defaultRetryPolicy

	resp, err := client.Do(req)

	if err != nil {
		setSessionFailure("ckmCommitRevisedTemplate: "+err.Error(), data)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 { // not success
		setSessionFailure("ERROR: ckmCommitRevisedTemplate response statuscode = "+strconv.FormatInt(int64(resp.StatusCode), 10), data)
		return false
	}
	loggingWrite(data, "ckmCommitRevisedTemplate : "+node.NodeName)
	// check local vs ckm
	// if ckm has later version, fail

	return true
}

func processTreeTopFirst(relation *nodeDefinition, isTop bool, nodeOrderList *[]string, data *sessionData, dryrun bool) bool {

	*nodeOrderList = append(*nodeOrderList, relation.NodeName)
	bumpparent := false

	if relation.NodeChanged > 0 {
		relation.NodeCommitIntended = 1
		loggingWrite(data, relation.NodeName+" has changed, so we intent to commit it [1]")
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
					loggingWrite(data, thechildnode.NodeName+" or its decendant has changed, so "+relation.NodeName+" needs a bump")

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

func ckmGetTemplateOET(node nodeDefinition, targetfile string, data *sessionData) error { // TODO: check return code / 404 issue

	loggingWrite(data, "ckmGetTemplateOET: "+targetfile)
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

	files := getLocalTemplateList(data.ChangesetFolder, data)

	if files == nil {
		return ""
	}

	for s := range data.WuaNodes {

		found := false

		for _, file := range files {
			loggingWrite(data, file)
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

	loggingWrite(data, "sendGraphDataToBrowser: "+"[ ["+nodes+"],["+edges+"] ]")

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

func sendProjectsToBrowser(data *sessionData) (status bool, projects string) {

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
		loggingWrite(data, "ERROR: ckm/rest/v1/projects response statuscode = "+strconv.FormatInt(int64(resp.StatusCode), 10))
		return false, ""
	}

	// need to read the PROJECT report....

	reportdata, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return false, ""
	}

	projectjson = string(reportdata)

	return true, projectjson
}

func getRootNodeText(path string, data *sessionData) string {

	rootnodetext := ""

	doc := etree.NewDocument()
	if err := doc.ReadFromFile(path); err != nil {
		setSessionFailure("getRootNodeText: "+err.Error()+": failed to read contents ("+path+")", data)
		return ""
	}

	var elTemplate *etree.Element
	//var theBaseAnnotations *etree.Element

	// get template structure
	elTemplate = doc.SelectElement("template")
	if elTemplate != nil {
		elDefinition := elTemplate.SelectElement("definition")

		if elDefinition != nil {
			rootnodetext = elDefinition.SelectAttrValue("name", "")
		}
	}

	return rootnodetext
}

func hasRootNodeBeenChanged(node nodeDefinition, data *sessionData) (state int, newtext string) {

	// find asset in mirror

	isFound, path := findTemplateInMirror(node, data)

	if isFound {

		existingtext := getRootNodeText(path, data)
		newtext := getRootNodeText(node.NodeLocation, data)
		/*
			if existingtext == "" || newtext == "" {
				setSessionFailure("hasRootNodeBeenChanged: problems finding rootnode text for "+node.NodeName+" (existing = "+existingtext+", new = "+newtext, data)
			} else { */
		if existingtext != newtext {
			return 1, newtext
		}
		/* } */

	}
	return 0, ""
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

			loggingWrite(data, "sendStatusToBrowser error:"+err.Error())
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

func backupTicket(data *sessionData) string {

	zipfile := data.ChangesetFolder + "/" + data.sessionConfig.WorkingFolderPath + "/" + data.sessionID + "_snapshot.zip"

	cmd := exec.Command("zip", "-r", zipfile, data.ChangesetFolder, "--exclude", "*.zip")
	var outbuf, errbuf bytes.Buffer
	cmd.Stdout = &outbuf
	cmd.Stderr = &errbuf

	err := cmd.Run()
	if err != nil {
		loggingWrite(data, "BackupTicket finished with error: "+err.Error())
	}

	stdout := outbuf.String()
	return stdout
}

// insertUpdatedRootNodes checks whether this node contains any tempaltes which have had
// their rootnode name/text edited and if so, amends the node to reflect the new edited text.
func insertUpdatedRootNodes(node *nodeDefinition, data *sessionData) bool {

	//  - find definition element
	// - find all items in definition with template id == subject template
	// - for each match, change or add the name attribute to match the subject

	path := node.NodeLocation
	Updated := false

	doc := etree.NewDocument()
	if err := doc.ReadFromFile(path); err != nil {
		setSessionFailure("insertUpdatedRootNodes: "+err.Error()+": failed to read contents ("+path+")", data)
		return false
	}

	var elTemplate *etree.Element

	// get template structure
	elTemplate = doc.SelectElement("template")
	if elTemplate != nil {
		elDefinition := elTemplate.SelectElement("definition")

		if elDefinition != nil {

			elDefinitionItemCollection := elDefinition.SelectElements("Item")

			for _, anItem := range elDefinitionItemCollection {
				embeddedTemplateId := anItem.SelectAttrValue("template_id", "")

				// check if this template has an updated node text
				for _, aNode := range data.WuaNodes {
					if aNode.NodeID == embeddedTemplateId && aNode.NodeRootEdited != 0 {
						// if so, update the subject node with the new text.
						nameAttr := anItem.SelectAttr("name")
						if nameAttr == nil { // will be nil if the element has never been renamed
							// need to create the name attr
							anItem.CreateAttr("name", aNode.NodeRootNewText)
						} else {
							nameAttr.Value = aNode.NodeRootNewText
						}
						Updated = true
					}
				}
			}
		}
	}

	if Updated {
		err := doc.WriteToFile(node.NodeLocation)
		if err != nil {
			setSessionFailure("insertUpdatedRootNodes: failed to write out updated template "+node.NodeName, data)
			return false
		}
		updateSessionStatus("insertUpdatedRootNodes: added to "+node.NodeName, data)
	}

	return true
}

// insertTraceability adds an annotation with traceability information to a given node
func insertTraceability(node *nodeDefinition, data *sessionData) bool {

	// don't process files that aren't local (i.e. don't muck about with the mirror files)
	if node.NodeIsLocal == 0 {
		return true
	}

	doc := etree.NewDocument()
	if err := doc.ReadFromFile(node.NodeLocation); err != nil {
		setSessionFailure("insertTraceability: "+err.Error()+": "+node.NodeName+" failed to read contents ("+node.NodeLocation+")", data)
		return false
	}

	var elTemplate *etree.Element
	var theBaseAnnotations *etree.Element

	// get template structure
	elTemplate = doc.SelectElement("template")
	if elTemplate != nil {
		elDefinition := elTemplate.SelectElement("definition")

		if elDefinition != nil {

			idxDefinition := elDefinition.Index()

			baseArchetype := elDefinition.SelectAttrValue("archetype_id", "")
			if baseArchetype != "" {
				loggingWrite(data, "insertTraceability: "+node.NodeName+" contains "+baseArchetype+" structure.")

				// does the template have annotations on the base node?

				elAnnotationsCollection := elTemplate.SelectElements("annotations")
				if elAnnotationsCollection != nil {
					for _, anAnnotationSet := range elAnnotationsCollection { // there may be multiple annotation elements with different xpaths
						pathAnnoation := anAnnotationSet.SelectAttr("path")
						if pathAnnoation.Value == "["+baseArchetype+"]" { // we're looking for the one for the main/base archetype
							// found it.
							theBaseAnnotations = anAnnotationSet
						}
					}
				}

				if theBaseAnnotations == nil {
					theBaseAnnotations = etree.NewElement("annotations")
					theBaseAnnotations.CreateAttr("path", "["+baseArchetype+"]")
					elTemplate.InsertChildAt(idxDefinition, theBaseAnnotations)
				}

				elBaseAnnotationItems := theBaseAnnotations.SelectElement("items")
				if elBaseAnnotationItems == nil {
					// create the annotation collection
					elBaseAnnotationItems = theBaseAnnotations.CreateElement("items")

				}

				// check all the item/keys
				elBaseAnnotationItemCollection := elBaseAnnotationItems.SelectElements("item")
				for _, anAnnotationPair := range elBaseAnnotationItemCollection {
					if strings.Contains(anAnnotationPair.SelectElement("value").Text(), "{~AHSID~") {
						// already existing.
						updateSessionStatus("traceability: already existing in "+node.NodeName, data)
						return true
					}
				}

				elAnnotationItem := elBaseAnnotationItems.CreateElement("item")
				elAnnotationItem.CreateElement("key").SetText("Technical.ﻩ Technical Traceability")
				elAnnotationItem.CreateElement("value").SetText("{~AHSID~" + node.NodeID + "~NAME~" + node.NodeName + "}")

				err := doc.WriteToFile(node.NodeLocation)
				if err != nil {
					setSessionFailure("insertTraceability: failed to write out updated template "+node.NodeName, data)
					return false
				}
				updateSessionStatus("insertTraceability: added to "+node.NodeName, data)
				return true
			}
		} else {
			setSessionFailure("insertTraceability: elDefinition := elTemplate.SelectElement() failed", data)
			return false

		}

	} else {
		setSessionFailure("insertTraceability: 1elTemplate = doc.SelectElement() failed", data)
		return false

	}

	return false
}
