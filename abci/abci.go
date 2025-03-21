package abci

type ABCIClientVersion int

const (
	ABCIClientVersion1 ABCIClientVersion = iota
	ABCIClientVersion2
)

func (v ABCIClientVersion) String() string {
	return []string{
		"ABCIClientVersion1",
		"ABCIClientVersion2",
	}[v]
}
