package channel

// The docker capability: the Agent talks to the local Docker Engine API
// (/var/run/docker.sock) for the Server and runs `docker compose` for the
// projects it finds. Streams that produce output (logs, pull, compose
// actions, a shell in a container) speak the shell capability's Message
// lines and end with Exit; the rest answer one JSON line.
const (
	CapabilityDocker = "docker"
	// KindDockerAPI is one allowed Engine API call; answers DockerAPIResult.
	KindDockerAPI = "docker.api"
	// KindDockerLogs streams a container's logs (DockerLogsParams).
	KindDockerLogs = "docker.logs"
	// KindDockerPull pulls an image and streams the progress (DockerPullParams).
	KindDockerPull = "docker.pull"
	// KindComposeList answers ComposeList: projects seen on running or
	// stopped containers plus those under the Agent's compose root.
	KindComposeList = "docker.compose.list"
	// KindComposeRead answers ComposeFile for a project.
	KindComposeRead = "docker.compose.read"
	// KindComposeWrite checks a compose file with `docker compose config` and
	// saves it (ComposeWriteParams); answers DockerResult.
	KindComposeWrite = "docker.compose.write"
	// KindComposeRun runs up / down / pull / restart and streams the output.
	KindComposeRun = "docker.compose.run"
	// KindDockerExec opens an interactive shell in a container on a PTY;
	// Messages as for the shell capability's terminal.
	KindDockerExec = "docker.exec"
	// ComposeRoot holds the projects made from the panel, one directory each.
	ComposeRoot = "/opt/tza-compose"
	// MaxComposeFile bounds a compose file.
	MaxComposeFile = 256 << 10
	// MaxDockerBody bounds an Engine API answer passed back to the Server.
	MaxDockerBody = 8 << 20
)

// DockerAPIParams is an Engine API request; the Agent only performs the ones
// on its allow list.
type DockerAPIParams struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  map[string]string `json:"query,omitempty"`
}

type DockerAPIResult struct {
	Status int    `json:"status"`
	Body   []byte `json:"body,omitempty"` // the Engine's JSON answer
	Error  string `json:"error,omitempty"`
}

type DockerLogsParams struct {
	Container  string `json:"container"`
	Tail       int    `json:"tail"`
	Follow     bool   `json:"follow"`
	Timestamps bool   `json:"timestamps"`
}

type DockerPullParams struct {
	Image string `json:"image"`
}

type DockerExecParams struct {
	Container string `json:"container"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

// ComposeProject is one compose project on the node.
type ComposeProject struct {
	Name    string   `json:"name"`
	Dir     string   `json:"dir"`
	Files   []string `json:"files"`
	Managed bool     `json:"managed"` // made from the panel, under ComposeRoot
	// Containers / Running count the project's containers.
	Containers int `json:"containers"`
	Running    int `json:"running"`
}

type ComposeList struct {
	Projects []ComposeProject `json:"projects"`
	// Compose is the `docker compose version` output, empty without the plugin.
	Compose string `json:"compose,omitempty"`
	Error   string `json:"error,omitempty"`
}

type ComposeParams struct {
	Project string `json:"project"`
}

type ComposeFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Error   string `json:"error,omitempty"`
}

// ComposeWriteParams saves Content as the project's compose file; a new
// project gets a directory under ComposeRoot.
type ComposeWriteParams struct {
	Project string `json:"project"`
	Content string `json:"content"`
}

// ComposeRunParams runs Action (up, down, pull, restart, delete) for a
// project. delete is down and then, for a project made from the panel, its
// directory; Volumes also removes the project's named volumes.
type ComposeRunParams struct {
	Project string `json:"project"`
	Action  string `json:"action"`
	Volumes bool   `json:"volumes,omitempty"`
}

type DockerResult struct {
	Error  string `json:"error,omitempty"`
	Output string `json:"output,omitempty"`
}
