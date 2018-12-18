package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"aqwari.net/xml/xmltree"
	"github.com/Tkanos/gonfig"
	"github.com/beevik/etree"
)

type childParentRelation struct {
	ChildName  string
	ChildId    string
	ParentName string
	ParentId   string
}

var relationset = []string{}
var relationsetXML = []string{}
var relationfile *os.File
var directory = "."

//var relationshipData = make([]string, 10)
var relationshipData = make(map[string]int) // where-used map
//var wumChildToParent = make(map[string]string) // where-used map
var wuaChildToParent = make([]childParentRelation, 0)

type Configuration struct {
	MirrorCkmPath     string
	ChangesetPath     string
	WorkingFolderPath string
	Port              string
	TestDataPath      string
}

func handler(w http.ResponseWriter, r *http.Request) {

	params := strings.Split(r.RequestURI, ",")

	configuration := Configuration{}
	err := gonfig.GetConf("config.json", &configuration)
	if err != nil {
		panic(err)
	}

	log.Printf("ckmpath = " + (string)(configuration.MirrorCkmPath))

	if len(params) < 3 {
		return
	}

	param0 := strings.Trim(params[0], "/") // operation type

	var templateID string
	var templateName string
	var changesetFolder string

	if param0 == "favicon.ico" {
		return
	}

	switch {
	case param0 == "report":
		templateID = params[1]   // child template internal id
		templateName = params[2] // child template name
		fmt.Fprintf(w, "<?xml version='1.0' encoding='UTF-8'?>")
		// fmt.Fprintf(w, "<?xml-stylesheet type='text/xsl' href='/GENERIC.xsl'?>")

	case param0 == "retrieve":

		templateID = params[1]                                          // child template internal id
		templateName = params[2]                                        // child template name
		changesetFolder = configuration.ChangesetPath + "/" + params[3] // ticket

	case param0 == "testdata":

		//templateID = params[1] // directory name
		//						templateName = params[2] // child template name
		//						changesetFolder = configuration.ChangesetPath + "/" + params[3]  // ticket

		loadTestData(configuration.TestDataPath)
		mapWhereUsedXML(relationsetXML)
		printMap()

		fmt.Fprintf(w, `
			
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
					font-size: 14px;
				  }
			
				  #cy {
					width: 1500px;
					height: 1000px;
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
						name: 'dagre'
					  },
			
					  style: [
						{
						  selector: 'node',
						  style: {
							'background-color': '#11479e',
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
			`)
		for s := range relationshipData {
			//{ data: { id: 'n0' } },
			fmt.Fprintf(w, "              { data: { id: '"+s+"' } },"+"\n")

		}
		fmt.Fprintf(w, `		],
						edges: [`)

		for _, r := range wuaChildToParent {
			//              { data: { source: 'n0', target: 'n1' } },
			fmt.Fprintf(w, `              { data: { source: '`+r.ChildName+"', target: '"+r.ParentName+"' } } ,"+"\n")

		}
		/*

		   g.addEdge('cherry', 'apple');
		   g.addEdge('strawberry', 'cherry');
		   g.addEdge('strawberry', 'apple');
		   g.addEdge('strawberry', 'tomato');
		   g.addEdge('tomato', 'apple');
		   g.addEdge('cherry', 'kiwi');
		   g.addEdge('tomato', 'kiwi');
		*/
		fmt.Fprintf(w, `
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
				
			 
			   `)
		return

	default:
		log.Printf("unknown operation type: " + param0)
		log.Printf("exiting...")
		return
	}

	relationsetXML = []string{} // used to store the relationships between files
	parentsExist := findParentTemplates(templateID, templateName, configuration.MirrorCkmPath)

	if param0 == "report" {
		for v := range relationsetXML {
			fmt.Fprintf(w, relationsetXML[v])
		}
	}

	if checkEnvironment(configuration, changesetFolder) == false {
		log.Printf("Exiting due to environment/config issues...")
		return
	}

	if param0 == "retrieve" {
		if parentsExist {
			log.Printf("Going to fetch parents....")
			parseParentsTree(relationsetXML, changesetFolder+"/"+configuration.WorkingFolderPath)
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

func checkEnvironment(config Configuration, ticketdir string) bool {

	// check that the dam folder is there

	// check that the ticket folder is inside the dam folder

	// make the working folder inside the ticket, if it's not there...

	os.MkdirAll(ticketdir+"/"+config.WorkingFolderPath, os.ModePerm)

	return true
}

func moveFiles(changesetFolder, assetType, WorkingFolderPath string) string {

	//cmd := exec.Command("rsync", "-av", "--ignore-existing", "--remove-source-files", "tempfiles/unzipped/templates", changesetFolder)
	cmd := exec.Command("rsync", "-av", "--ignore-existing", "--remove-source-files", changesetFolder+"/"+WorkingFolderPath+"/unzipped/"+assetType, changesetFolder)
	var outbuf, errbuf bytes.Buffer
	cmd.Stdout = &outbuf
	cmd.Stderr = &errbuf

	err := cmd.Run()
	log.Printf("moveFiles finished with error: %v", err)
	stdout := outbuf.String()
	return stdout

	//	err := os.Rename( "tempfiles/unzipped/templates", ticket + "/")
	//check(err)

}

func ckmGetTemplateFilepackURL(cid string) (filesetURL string) {

	if data, err := ckmGetContent2("https://ahsckm.ca/ckm/rest/v1/templates/" + cid + "/file-set-url"); err != nil {
		log.Printf("Failed to get XML: %v", err)
	} else {
		check(err)
		log.Println("Received XML:")
		log.Println(string(data))
		return string(data)
	}
	return ""
}

func ckmGetCidFromID(id string) (cid string) {

	if data, err := ckmGetContent2("https://ahsckm.ca/ckm/rest/v1/templates/citeable-identifier/" + id); err != nil {
		log.Printf("Failed to get XML: %v", err)
	} else {
		check(err)
		log.Println("Received XML:")
		log.Println(string(data))
		return string(data)
	}

	return ""
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

		cid := ckmGetCidFromID(templateid)
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

func ckmGetContent2(url string) ([]byte, error) {
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
	} else {
		//log.Println(string(data))
	}

	return data, nil
}

func loadTestData(path string) []string {

	var files []string

	root := path
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {

		if filepath.Ext(info.Name()) == ".xml" {
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
			mapWhereUsedXML(testdata)
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
	http.HandleFunc("/GENERIC.xsl", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("SERVING /home/coni/node/Dropbox/AHS/CMIO Office/Clinical Content/XSLT/GENERIC.xslt")
		http.ServeFile(w, r, "/home/coni/node/Dropbox/AHS/CMIO Office/Clinical Content/XSLT/GENERIC.xslt")
	})

	configuration := Configuration{}
	err := gonfig.GetConf("config.json", &configuration)
	if err != nil {
		panic(err)
	}

	http.ListenAndServe(":"+configuration.Port, nil)

}

func findTemplateID(path string) string {
	// return the unique identifier for the template specified in the path param

	var templateID string

	log.Printf("findTemplateID - " + path)

	var _result = grepFile(path, "<id>")
	log.Printf("findTemplateID result " + _result)

	r := strings.NewReplacer("<id>", "", "</id>", "")

	templateID = r.Replace(_result)
	templateID = strings.TrimSpace(templateID)

	return templateID
}

func findParentTemplates(id string, file string, ckmMirror string) bool {
	// find names of templates that contain id

	if id == "" {
		log.Printf("findParentTemplates failure....no id passed in")
		return false
	}

	var foundfiles = grepDir(id, ckmMirror)

	log.Printf("findParentTemplates( " + id + ") parents = (" + foundfiles + ")")

	results := strings.Split(foundfiles, "\n")

	relationsetXML = append(relationsetXML, "<template><filename>"+file+"</filename><id>"+id+"</id><contained-in>")

	parent := ""

	for i := range results {
		result := results[i]
		parts := strings.Split(result, ":")
		parent = parts[0]
		parent = strings.TrimSpace(parent)

		if parent != "" {
			fmt.Println("findParentTemplates parent - " + parent)
			id = findTemplateID(parent)
			trimmedparent := filepath.Base(parent)
			findParentTemplates(id, trimmedparent, ckmMirror)
		}
	}

	relationsetXML = append(relationsetXML, "</contained-in></template>")

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
	log.Printf("grepDir finished with error: %v", err)
	stdout := outbuf.String()
	return stdout
}

func grepFile(file string, pattern string) string {

	cmd := exec.Command("grep", pattern, file)

	var outbuf, errbuf bytes.Buffer
	cmd.Stdout = &outbuf
	cmd.Stderr = &errbuf

	err := cmd.Run()
	log.Printf("grepFile finished with error: %v", err)
	stdout := outbuf.String()
	return stdout
}

func printMap() {

	b, err := json.MarshalIndent(wuaChildToParent, "", "  ")
	if err != nil {
		fmt.Println("error:", err)
	}
	fmt.Print(string(b))

}

// navigates through xml tree recursively and appends child->parent relationships to map
func mapTemplate(el *etree.Element) bool {

	// TODO: multiple 'contained' templates
	eTemplateFilename := el.SelectElement("filename")
	eTemplateId := el.SelectElement("id")

	sCurrentTemplateFilename := eTemplateFilename.Text()
	sCurrentTemplateId := eTemplateId.Text()

	//var found = false

	// add to unique list of known templates
	relationshipData[sCurrentTemplateFilename] = 0

	log.Printf("filename : " + sCurrentTemplateFilename)

	eContainedIn := el.SelectElement("contained-in")

	if eContainedIn != nil {
		slParents := eContainedIn.SelectElements("template")

		for _, eParentTemplate := range slParents {

			//log.Printf("contained-in : " + eContainedIn.Text())

			if eParentTemplate != nil {
				eParentFilename := eParentTemplate.SelectElement("filename")
				eParentId := eParentTemplate.SelectElement("id")

				var sParentFilename = ""
				var sParentId = ""

				if eParentFilename != nil {
					sParentFilename = eParentFilename.Text()
				}

				if eParentId != nil {
					sParentId = eParentId.Text()
				}

				var already = false

				if sParentFilename != "" {
					log.Printf("contained_filename : " + eParentFilename.Text())

					for _, r := range wuaChildToParent {
						if (r.ChildId == sCurrentTemplateId) && (r.ParentId == sParentId) {
							already = true
						}
					}

					if !already {
						//wumChildToParent[templateid.Text()] = sParentFilename
						var relation childParentRelation
						relation.ChildName = sCurrentTemplateFilename
						relation.ChildId = sCurrentTemplateId
						relation.ParentName = sParentFilename
						relation.ParentId = sParentId
						wuaChildToParent = append(wuaChildToParent, relation)
					}

					mapTemplate(eParentTemplate)
				}

			}
		}
	}
	// eContainedIn := el.SelectElement("contained-in")

	// if eContainedIn == nil {
	// 	return true
	// }

	return true
}

func mapWhereUsedXML(ParentTree []string) {

	// TODO: multiple root templates

	doc := etree.NewDocument()
	if err := doc.ReadFromString(strings.Join(ParentTree, "\x20")); err != nil {
		panic(err)
	}

	templates := doc.SelectElements("template")

	for _, template := range templates {
		if template != nil {
			mapTemplate(template)
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
