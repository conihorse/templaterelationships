package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"aqwari.net/xml/xmltree"
	"github.com/Tkanos/gonfig"
	"github.com/beevik/etree"

	//	"github.com/gorilla/sessions"
	"github.com/hashicorp/go-retryablehttp"
	"github.com/segmentio/ksuid"
)

type nodeDefinition struct {
	uid             string   // unique id of this relationship (not used ATM)
	NodeName        string   // the filename of the template (not path)
	NodeLocation    string   // the relative path to the node file
	NodeID          string   // internal id of the template
	NodeHash        string   // the md5 hash of the local template
	NodeIsLeaf      int      // = 1 if node is a leaf
	NodeCommitOrder int      // order in which the node should be commmitted to ckm [-1,n : unknown, 0-n order)
	NodeIsCommitted int      // [-1,0,1 : unknown, failed, succeeded]
	NodeChanged     int      // flag set if NodeHash different to ckm version [ -1,0,1,2 : unknown,not changed,changed,new ]
	NodeCID         string   // ckm citable identifier for the template (blank if template is new)
	NodeParentList  []string // list of parent template filenames
}

type sessionData struct {
	sessionID        string
	WuaNodes         []nodeDefinition // working structure holding nodes for proceessing and graph generation.
	relationset      []string
	relationsetXML   []string
	relationfile     *os.File
	treeCommitOrder  int
	relationshipData map[string]int
	htmlGraph        string
	userStateInfo    string
	statusText       string
	//statusChannel    chan string
	isFinished bool
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
}

// Note: Don't store your key in your source code. Pass it via an
// environmental variable, or flag (or both), and don't accidentally commit it
// alongside your code. Ensure your key is sufficiently random - i.e. use Go's
// crypto/rand or securecookie.GenerateRandomKey(32) and persist the result.
//var store = sessions.NewCookieStore([]byte("essionkey"))

func buildStatusPage(data sessionData) string {

	return data.statusText
}

func getSessionData(sessionID string) *sessionData {

	for _, data := range gSessionDataList {
		if data.sessionID == sessionID {
			return data
		}
	}
	return nil

}

func handlerStatus(w http.ResponseWriter, r *http.Request) {

	status := "testing"
	fmt.Fprintf(w, "<h3>Status "+status+"</h3>")
}

func handler(w http.ResponseWriter, r *http.Request) {

	var templateID string
	var templateName string
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



	configuration := configuration{}
	err := gonfig.GetConf("config.json", &configuration)
	if err != nil {
		panic(err)
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

	thisSessionData.WuaNodes = make([]nodeDefinition, 0)
	sessionID := ksuid.New().String()
	thisSessionData.relationshipData = make(map[string]int) // where-used map
	thisSessionData.sessionID = sessionID
	thisSessionData.relationsetXML = []string{} // used to store the relationships between files

	log.Printf("ckmpath = " + (string)(configuration.MirrorCkmPath))

	switch {

	case param0 == "precommit":
		changesetFolder = configuration.ChangesetPath + "/" + params[1] // ticket'

		//gStatusChan = make(chan string, 1000)

		//gDataChan = make(chan sessionData, 100)

		//thisSessionData.statusChannel = make(chan string, 1000)
		//session.Values[sessionID] = thisSessionData
		thisSessionData.isFinished = false
		// Save it before we write to the response/return from the handler.
		//session.Save(r, w)

		gSessionDataList = append(gSessionDataList, &thisSessionData)

		template, err := readLines(configuration.HTMLStatusTemplate)
		if err == nil {

			var line string
			for i := range template {
				line = template[i]
				line = strings.Replace(line, "%%TICKET%%", changesetFolder, -1)
				line = strings.Replace(line, "%%SESSIONID%%", thisSessionData.sessionID, -1)
				fmt.Fprintln(w, line)
			}
		}

		go precommitProcessing(changesetFolder, configuration.MirrorCkmPath, &thisSessionData)

		return

	case param0 == "commit":

		return

	case param0 == "ticket-retrieve-supporting":

		templateID = params[1]                                          // child template internal id
		templateName = params[2]                                        // child template name
		changesetFolder = configuration.ChangesetPath + "/" + params[3] // ticket'

	case param0 == "ticket-view-report":
		changesetFolder = configuration.ChangesetPath + "/" + params[1] // ticket'

		// for every template in the changeset
		// find parent templates
		// add xml to map

		mapTicketTemplates("./"+changesetFolder+"/", configuration.MirrorCkmPath, &thisSessionData)
		printMap(thisSessionData)
		fmt.Fprintln(w, generateMap(thisSessionData))

		// return map?
		return

	default:
		log.Printf("unknown operation type: " + param0)
		log.Printf("exiting...")
		return
	}

	parentsExist := findParentTemplates(templateID, templateName, configuration.MirrorCkmPath, changesetFolder, &thisSessionData)

	if param0 == "template-xml-report" {
		for v := range thisSessionData.relationsetXML {
			fmt.Fprintf(w, thisSessionData.relationsetXML[v])
		}
	}

	if checkEnvironment(configuration, changesetFolder) == false {
		log.Printf("Exiting due to environment/config issues...")
		return
	}

	if param0 == "ticket-retrieve-supporting" {
		if parentsExist {
			log.Printf("Going to fetch parents....")
			parseParentsTree(thisSessionData.relationsetXML, changesetFolder+"/"+configuration.WorkingFolderPath)
			status := ""

			status = moveFiles(changesetFolder, "templates", configuration.WorkingFolderPath)
			log.Printf(status)
			fmt.Fprintf(w, "<h3>grabbed"+status+"</h3>")

			status = moveFiles(changesetFolder, "archetypes", configuration.WorkingFolderPath)
			log.Printf(status)
			fmt.Fprintf(w, "<h3>grabbed"+status+"</h3>")
		}
	}

}

func updateSessionStatus(status string, data *sessionData) {
	log.Println("updateSessionStatus-------------------------------------------------------------------------")
	//data.statusChannel <- status
	data.statusText = status
	//gDataChan <- data
	//gStatusText = status
	//gStatusChan <- status
}

func precommitProcessing(changesetFolder string, mirrorpath string, data *sessionData) {

	updateSessionStatus("Building template maps", data)

	mapTicketTemplates("./"+changesetFolder+"/", mirrorpath, data)

	// find leaf

	updateSessionStatus("Finding leaf nodes [ "+string(len(data.WuaNodes))+"]", data)

	// for each child, check if it is a parent
	for i := 0; i < len(data.WuaNodes); i++ {
		var isLeaf = 1

		for j := 0; j < len(data.WuaNodes); j++ {

			for k := 0; k < len(data.WuaNodes[j].NodeParentList); k++ {
				if data.WuaNodes[j].NodeParentList[k] == data.WuaNodes[i].NodeName {
					// child is not a leaf
					isLeaf = 0
					break
				}
			}
		}
		data.WuaNodes[i].NodeIsLeaf = isLeaf
	}

	updateSessionStatus("Getting template metadata from CKM (hashes, cids, etc)", data)

	getHashAndChangedStatus(changesetFolder, data)

	data.treeCommitOrder = 0

	updateSessionStatus("Committing assets", data)

	// walk up tree, starting at each leaf node
	for i := 0; i < len(data.WuaNodes); i++ {
		// for each leaf, follow the tree up
		if data.WuaNodes[i].NodeIsLeaf == 1 {
			if !walkTree(&data.WuaNodes[i], true, *data) {
				log.Println("ERROR : process failed...")
			}
		}
	}
	//fmt.Fprintln(w, generateMap(*data))

	updateSessionStatus("*** Done! ***", data)

	printMap(*data)

	data.htmlGraph = generateMap2(*data, "graphtemplate.html")
	data.isFinished = true

}

func checkEnvironment(config configuration, ticketdir string) bool {

	// check that the dam folder is there

	// check that the ticket folder is inside the dam folder

	// make the working folder inside the ticket, if it's not there...

	os.MkdirAll(ticketdir+"/"+config.WorkingFolderPath, os.ModePerm)

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

	//	err := os.Rename( "tempfiles/unzipped/templates", ticket + "/")
	//check(err)

}

func ckmGetTemplateFilepackURL(cid string) (filesetURL string) {

	if data, err := ckmGetContentPlain("https://ahsckm.ca/ckm/rest/v1/templates/" + cid + "/file-set-url"); err != nil {
		log.Printf("Failed to get XML: %v", err)
	} else {
		check(err)
		log.Println("Received XML:" + string(data))
		return string(data)
	}
	return ""
}

// TODO handle new templates (those dont exist in ckm, so it will return a "resource could not be found")
func ckmGetCidFromID(id string) (status bool, cid string) {

	req, err := http.NewRequest("GET", "https://ahsckm.ca/ckm/rest/v1/templates/citeable-identifier/"+id, nil)
	if err != nil {
		return false, ""
	}
	req.Header.Set("Accept", "text/plain")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 { // not success
		log.Println("ERROR: ckmGetCidFromID response statuscode = " + strconv.FormatInt(int64(resp.StatusCode), 10))
		return false, ""
	}

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		println()
		return false, ""
	}

	return true, string(data)
}

func parseParentsTree(ParentTree []string, TicketWorkingFolderPath string) {

	stringByte := strings.Join(ParentTree, "\x20") // x20 = space and x00 = null
	root, err := xmltree.Parse([]byte(stringByte))
	if err != nil {
		log.Fatal(err)
	}
	for _, el := range root.Search("", "id") {

		templateid := (string)(el.Content)
		// get cid

		templateexists, cid := ckmGetCidFromID(templateid)

		if templateexists {

			log.Printf("id: " + templateid + " cid: " + cid)
			// get template filepack url

			filepack := ckmGetTemplateFilepackURL(cid)
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
func check(e error) {
	if e != nil {
		panic(e)
	}
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
	resp, err := http.Get(url)
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

func ckmGetContentPlain(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	check(err)
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("Authorization", "Basic am9uLmJlZWJ5OlBhNTV3b3Jk")

	resp, err := http.DefaultClient.Do(req)
	check(err)
	defer resp.Body.Close()
	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("Read body: %v", err)
	}
	return data, nil
}

func ckmGetContentXML(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	check(err)
	req.Header.Set("Accept", "application/xml")
	req.Header.Set("Authorization", "Basic am9uLmJlZWJ5OlBhNTV3b3Jk")

	resp, err := http.DefaultClient.Do(req)
	check(err)
	defer resp.Body.Close()
	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("Read body: %v", err)
	}
	return data, nil
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

func main() {

	http.HandleFunc("/", handler)
	http.HandleFunc("/status/*", handlerStatus)

	http.HandleFunc("/assets", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "/GENERIC.xslt")
	})


	http.HandleFunc("/GENERIC.xsl", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("SERVING /home/coni/node/Dropbox/AHS/CMIO Office/Clinical Content/XSLT/GENERIC.xslt")
		http.ServeFile(w, r, "/home/coni/node/Dropbox/AHS/CMIO Office/Clinical Content/XSLT/GENERIC.xslt")
	})

	configuration := configuration{}
	err := gonfig.GetConf("config.json", &configuration)
	if err != nil {
		panic(err)
	}

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
func findParentTemplates(id string, file string, ckmMirror string, ticketDir string, data *sessionData) bool {

	if id == "" {
		log.Printf("findParentTemplates failure....no id passed in")
		return false
	}

	var foundfiles = grepDir(id, ckmMirror)

	log.Printf("findParentTemplates( " + id + " / " + file + " ) ckm parents = (" + foundfiles + ")")
	var foundlocalfiles = grepDir(id, ticketDir)
	log.Printf("findParentTemplates( " + id + " / " + file + " ) local parents = (" + foundlocalfiles + ")")
	results := strings.Split(foundlocalfiles+"\n"+foundfiles, "\n")
	data.relationsetXML = append(data.relationsetXML, "<template><filename>"+file+"</filename><id>"+id+"</id><contained-in>")
	parent := ""

	for i := range results {
		result := results[i]
		parts := strings.Split(result, ":")
		parent = parts[0]
		parent = strings.TrimSpace(parent)

		if parent != "" {
			log.Println("findParentTemplates parent - " + parent)
			id = findTemplateID(parent)
			trimmedparent := filepath.Base(parent)

			findParentTemplates(id, trimmedparent, ckmMirror, ticketDir, data)
		}
	}
	data.relationsetXML = append(data.relationsetXML, "</contained-in></template>")

	if len(results) > 1 {
		return true
	}
	return false
}

func grepDir(pattern string, ckmMirror string) string {
	cmd := exec.Command("grep", "-r", "template_id=\""+pattern, ckmMirror)
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

func printSessionData(data sessionData) string {

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
	sCurrentTemplateI := eTemplateID.Text()

	// add to unique list of known templates
	data.relationshipData[sCurrentTemplateFilename] = 0

	eContainedIn := el.SelectElement("contained-in")

	if eContainedIn != nil {
		slParents := eContainedIn.SelectElements("template")

		var alreadyExists = false
		var foundrelation = -1
		var relation nodeDefinition
		// find the child
		for idx, r := range data.WuaNodes {
			if r.NodeID == sCurrentTemplateI {
				alreadyExists = true
				foundrelation = idx
				break
			}
		}

		if !alreadyExists { // create node if not found
			relation.uid = ksuid.New().String()
			relation.NodeName = sCurrentTemplateFilename
			relation.NodeID = sCurrentTemplateI
			relation.NodeChanged = -1     // not checked.
			relation.NodeCommitOrder = -1 // not yet processed by precommit
			relation.NodeIsCommitted = -1
			data.WuaNodes = append(data.WuaNodes, relation)
			//addSessionNode(gTempSessionID,&relation)

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

func generateMap(data sessionData) string {

	graphmap := `
		
		<html>
		
		  <head>
			<title>cytoscape-dagre.js demo</title>
		
			<meta name="viewport" content="width=device-width, user-scalable=no, initial-scale=1, maximum-scale=1">
		
			<script src="https://unpkg.com/cytoscape/dist/cytoscape.min.js"></script>
		
			<!-- for testing with local version of cytoscape.js -->
			<!--<script src="../cytoscape.js/build/cytoscape.js"></script>-->
		
			<script src="https://unpkg.com/dagre@0.7.4/dist/dagre.js"></script>
			<script src="https://cytoscape.org/cytoscape.js-dagre/cytoscape-dagre.js"></script>
		
			<style>
			  body {
				font-family: helvetica;
				font-size: 10px;
			  }
		
			  #cy {
				width: 100%;
				height: 100%;
				position: absolute;
				left: 0;
				top: 0;
				z-index: 999;
			  }
		
			  h1 {
				opacity: 0.5;
				font-size: 1em;
			  }
			</style>
		
			<script>
			  window.addEventListener('DOMContentLoaded', function(){
		
				var cy = window.cy = cytoscape({
				  container: document.getElementById('cy'),
		
				  boxSelectionEnabled: false,
				  autounselectify: true,
		
				  layout: {
					name: 'dagre',
					rankDir: 'BT',
					labelpos: 'R',
									
				  },
		
				  style: [
					{
					  selector: 'node',
					  style: {
						//'background-color': '#11479e',
						'background-color': '#ff0000',
						'text-valign': 'center',
						'text-halign': 'right',
							   'label': 'data(id)'
					  }
					},
		
					{
					  selector: 'edge',
					  style: {
						'width': 4,
						'target-arrow-shape': 'triangle',
						'line-color': '#9dbaea',
						'target-arrow-color': '#9dbaea',
						'curve-style': 'bezier'
					  }
					}
				  ],

				  elements: {
					nodes: [
		`
	for s := range data.relationshipData {
		//{ data: { id: 'n0' } },
		graphmap += "              { data: { id: '" + s + "' } }," + "\n"

	}
	graphmap += `		],
					edges: [`

	for _, r := range data.WuaNodes {
		//              { data: { source: 'n0', target: 'n1' } },
		for _, p := range r.NodeParentList {
			graphmap += `              { data: { source: '` + r.NodeName + "', target: '" + p + "' } } ," + "\n"
		}

	}

	graphmap += `
							]
						}
					});
			
				  });
				</script>
			  </head>
			
			  <body>
				<h1>cytoscape-dagre demo</h1>
			
				<div id="cy"></div>
			
			  </body>
			
			</html>
			
		 
		   `

	return graphmap

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
		panic(err)

	}

	return files

}

func mapTicketTemplates(ticketPath, mirrorPath string, data *sessionData) {

	var files []string
	files = getLocalTemplateList(ticketPath)
	if files != nil {

		for _, file := range files {
			log.Printf("loadTicketTemplates: " + file)
			updateSessionStatus("Mapping Asset : "+file, data)

			templateID := findTemplateID(file)
			findParentTemplates(templateID, filepath.Base(file), mirrorPath, ticketPath, data)
			mapWhereUsedXML(data.relationsetXML, data)
		}
	}

}

// get hashs for local and ckm files, compare them and update NodeChanged status on node
// TODO: deal with new templates that are not in CKM yet (NodeChanged = 2)
func getHashAndChangedStatus(ticketPath string, data *sessionData) {

	var files []string
	files = getLocalTemplateList(ticketPath)
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

				templateexists, cid := ckmGetCidFromID(data.WuaNodes[i].NodeID)
				if templateexists {
					data.WuaNodes[i].NodeCID = cid
					ckmHash := ckmGetHash(data.WuaNodes[i].NodeCID)
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

func ckmGetHash(cid string) string {
	if data, err := ckmGetContentXML("https://ahsckm.ca/ckm/rest/v1/templates/" + cid + "/hash"); err != nil {
		log.Printf("Failed to get XML: %v", err)
	} else {
		check(err)
		log.Println("Received XML:" + string(data))
		return string(data)
	}

	return ""
}

// TODO: check validation report from ckm
func ckmValidateTemplate(node *nodeDefinition) bool {

	templatesource, err := readLines(node.NodeLocation)

	if err != nil {
		return false
	}

	body := strings.NewReader(strings.Join(templatesource, "\x20"))
	req, err := http.NewRequest("POST", "https://ahsckm.ca/ckm/rest/v1/templates/validation-report", body)
	if err != nil {
		// handle err
		return false
	}
	req.Header.Set("Authorization", "Basic am9uLmJlZWJ5OlBhNTV3b3Jk")
	req.Header.Set("Accept", "application/xml")
	req.Header.Set("Content-Type", "application/xml")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// handle err
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 { // not success
		log.Println("ERROR: validation-report response statuscode = " + strconv.FormatInt(int64(resp.StatusCode), 10))
		return false
	}

	log.Println("validated : " + node.NodeLocation)

	return true
}

// commit a template revision to ckm [NOTE: see also ckmCommitRevisedTemplate() ]
// TODO : log message
// TODO : template type
// TODO : project cid
// TODO : capture new resource info returned from ckm (for the report...)
func ckmCommitNewTemplate(node *nodeDefinition) bool {
	logmessage := "testing%20commit%20process"
	templatesource, err := readLines(node.NodeLocation)
	if err != nil {
		return false
	}
	body := strings.NewReader(strings.Join(templatesource, "\x20"))

	templatetype := "ORDER_ITEM"
	projectcid := "1175.115.78"

	theRequest := "https://ahsckm.ca/ckm/rest/v1/templates?template-type=" + templatetype + "&cid-project=" + projectcid + "&log-message=" + logmessage + "&proceed-if-outdated-resources-used=false"

	req, err := retryablehttp.NewRequest("POST", theRequest, body)
	//req, err := http.NewRequest("POST", theRequest, body)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "application/xml")
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Authorization", "Basic am9uLmJlZWJ5OlBhNTV3b3Jk")

	//resp, err := http.DefaultClient.Do(req)
	client := retryablehttp.NewClient()
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 { // not success
		log.Println("ERROR: ckmCommitNewTemplate response statuscode = " + strconv.FormatInt(int64(resp.StatusCode), 10))
		log.Println(" theRequest = " + theRequest)
		log.Println(" theBody = " + strings.Join(templatesource, "\x20"))
		return false
	}

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
	if resp.StatusCode == 0 || (resp.StatusCode >= 400 && resp.StatusCode != 501) {
		return true, nil
	}

	return false, nil
}

// commit a template revision to ckm [NOTE: see also ckmCommitNewTemplate() ]
// TODO : log message
// TODO : capture new resource info returned from ckm (for the report...)
func ckmCommitRevisedTemplate(node *nodeDefinition) bool {
	logmessage := "testing%20commit%20process"
	templatesource, err := readLines(node.NodeLocation)
	if err != nil {
		return false
	}
	body := strings.NewReader(strings.Join(templatesource, "\x20"))
	//req, err := http.NewRequest("PUT", "https://ahsckm.ca/ckm/rest/v1/templates/"+node.NodeCID+"?log-message="+logmessage+"&proceed-if-outdated-resources-used=false", body)
	req, err := retryablehttp.NewRequest("PUT", "https://ahsckm.ca/ckm/rest/v1/templates/"+node.NodeCID+"?log-message="+logmessage+"&proceed-if-outdated-resources-used=false", body)

	if err != nil {
		// handle err
		return false
	}
	req.Header.Set("Accept", "application/xml")
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Authorization", "Basic am9uLmJlZWJ5OlBhNTV3b3Jk")

	//resp, err := http.DefaultClient.Do(req)
	client := retryablehttp.NewClient()
	client.CheckRetry = defaultRetryPolicy

	resp, err := client.Do(req)

	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 { // not success
		log.Println("ERROR: ckmCommitRevisedTemplate response statuscode = " + strconv.FormatInt(int64(resp.StatusCode), 10))
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

func walkTree(relation *nodeDefinition, isLeaf bool, data sessionData) bool {

	var allChildrenCommitted = true

	if !isLeaf { // (leaf nodes have no children)
		// check no children not committed
		for i := 0; i < len(data.WuaNodes); i++ { // for all nodes
			for j := 0; j < len(data.WuaNodes[i].NodeParentList); j++ { // for all parents of this node
				if data.WuaNodes[i].NodeParentList[j] == relation.NodeName { // is a child of this node
					if data.WuaNodes[i].NodeIsCommitted != 1 {
						// child not yet committed
						allChildrenCommitted = false
						break
					}
				}
			}
		}
	}
	if allChildrenCommitted { // only commit this node if all of its children have already been commmitted.

		if relation.NodeChanged > 0 { // only validate templates that are different to ckm version
			if !ckmValidateTemplate(relation) {
				log.Println("walkTree : ERROR validate template failed for " + relation.NodeName)
				return false
			}
		}

		// NOTE: we need to commit unchanged templates, so that they receive their child's updates in ckm

		// commit child
		if relation.NodeChanged == 1 { // commit revision to existing template
			if !ckmCommitRevisedTemplate(relation) {
				log.Println("walkTree : ERROR ckmCommitRevisedTemplate failed for " + relation.NodeName)
				return false
			}
		} else if relation.NodeChanged == 2 { // commit new template
			if !ckmCommitNewTemplate(relation) {
				log.Println("walkTree : ERROR ckmCommitNewTemplate failed for " + relation.NodeName)
				return false
			}
		}

		relation.NodeCommitOrder = data.treeCommitOrder // global var
		relation.NodeIsCommitted = 1                    // node has been committed
		data.treeCommitOrder++
		// find parents
		for _, aParent := range relation.NodeParentList {
			for i := 0; i < len(data.WuaNodes); i++ {
				// process each parent of this node
				if data.WuaNodes[i].NodeName == aParent {
					if !walkTree(&data.WuaNodes[i], false, data) {
						return false
					}
				}
			}
		}
	}
	return true
}

func sendReportToBrowser(statusSessionID string) string {

	//return "{\"the report\": \"" + "dump" + "\"}"
	data := getSessionData(statusSessionID)

	if data != nil {
		return printMap(*data)
	}
	return ""

}

func sendStatusToBrowser(statusSessionID string) string {

	data := getSessionData(statusSessionID)

	/* 	if data != nil {
		status := data.statusText
		if data.isFinished {
			fmt.Fprintln(w, printMap(*data))
		} else {
			fmt.Fprintln(w, "{\"last status\": \""+status+"\"}")
		}
	} */

	if data != nil {
		status := data.statusText
		/* 		if data.isFinished {
		   			return printSessionData(*data)
		   		} else {
		*/return "{\"last status\": \"" + status + "\"}"
		//	}
	}

	return ""
}
