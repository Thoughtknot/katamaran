package node

import (
	"fmt"
	. "katamaran/pkg/data"
	msg "katamaran/pkg/msg/pkg"
	"katamaran/pkg/plist"
	"math/rand"
	"time"

	"golang.org/x/exp/constraints"
)

const ElectionTimeout = time.Second * 10

const (
	Follower State = iota
	Candidate
	Leader
)

type Node struct {
	id            CandidateId
	leaderId      *CandidateId
	state         State
	list          plist.Data
	commitIndex   Index
	lastApplied   Index
	nextIndex     map[CandidateId]Index
	matchIndex    map[CandidateId]Index
	nextHeartBeat time.Time
	lastSeen      time.Time
	sender        Sender
}

type Sender interface {
	GetCandidates() []CandidateId
	RequestAllVotes(Term, CandidateId, Index, Term) bool
	SendAppendEntries(string, Term, CandidateId, Index, Term, []Entry, Index) (Term, bool)
}

func min[T constraints.Ordered](a, b T) T {
	if a < b {
		return a
	}
	return b
}

func MakeNode(id CandidateId, sender Sender) Node {
	n := Node{
		id:          id,
		leaderId:    nil,
		list:        plist.MakeNList(id),
		commitIndex: 0,
		lastApplied: -1,
		nextIndex:   make(map[CandidateId]Index),
		matchIndex:  make(map[CandidateId]Index),
		sender:      sender,
	}
	for _, v := range sender.GetCandidates() {
		n.nextIndex[v] = 0
		n.matchIndex[v] = 0
	}
	return n
}

func (n *Node) getLastEntry() Entry {
	return n.list.GetLastEntry()
}

func (n *Node) AddEntry(value []byte) bool {
	if n.state != Leader {
		return false
	}
	n.list.Add(Entry{Value: value, Index: n.list.GetNextIndex(), Term: n.list.GetCurrentTerm()})
	return true
}

func (n *Node) Tick() {
	fmt.Println("Tick", n.state, n.commitIndex, n.list.GetCurrentTerm(), n.list.GetNextIndex())
	if n.state == Leader {
		if time.Now().After(n.nextHeartBeat) {
			//fmt.Println("Leader - sending heartbeat")
			n.sendHeartbeat()
			n.nextHeartBeat = time.Now().Add(ElectionTimeout / 3)
		}
		lastEntry := n.getLastEntry()
		allEntries := n.list.GetAll()
		for k, v := range n.nextIndex {
			if lastEntry.Index < v {
				continue
			}
			entries := allEntries[v:]
			var prev Entry
			if v == 0 {
				prev = Entry{Value: nil, Index: -1, Term: 0}
			} else {
				prev = allEntries[v-1]
			}

			_, success := n.sender.SendAppendEntries(string(k), n.list.GetCurrentTerm(), n.id, prev.Index, prev.Term, entries, n.commitIndex)
			if success {
				n.nextIndex[k] = lastEntry.Index + 1
				n.matchIndex[k] = lastEntry.Index + 1
			} else if v > 0 {
				n.nextIndex[k] = v - 1
			}

		}
		for n.commitIndex < Index(len(allEntries)) {
			matches := 0
			for _, v := range n.matchIndex {
				if v > n.commitIndex {
					matches++
				}
			}
			if matches >= len(n.matchIndex)/2 && allEntries[n.commitIndex].Term == n.list.GetCurrentTerm() {
				n.commitIndex++
			} else {
				break
			}
		}
	} else if time.Since(n.lastSeen) > ElectionTimeout*2 {
		fmt.Println("Election timeout - converting to candidate")
		n.convertToCandidate()
	}
}

func (n *Node) sendHeartbeat() {
	for k, v := range n.nextIndex {
		var prev Entry
		if v == 0 {
			prev = Entry{Value: nil, Index: -1, Term: 0}
		} else {
			prev = n.list.Get(v - 1)
		}
		n.sender.SendAppendEntries(string(k), n.list.GetCurrentTerm(), n.id, prev.Index, prev.Term, make([]Entry, 0), n.commitIndex)
	}
}

func (n *Node) startElection() {
	nextTerm := n.list.GetCurrentTerm() + 1
	n.list.SetCurrentTerm(nextTerm)
	n.list.SetVotedFor(&n.id)
	n.lastSeen = time.Now()
	entry := n.getLastEntry()
	if n.sender.RequestAllVotes(n.list.GetCurrentTerm(), n.id, entry.Index, entry.Term) {
		n.convertToLeader()
	}
}

func (n *Node) convertToCandidate() {
	fmt.Println("Candidate")
	n.state = Candidate
	n.startElection()
}

func (n *Node) convertToLeader() {
	fmt.Println("Leader")
	n.state = Leader
}

func (n *Node) convertToFollower() {
	fmt.Println("Follower")
	n.state = Follower
	n.receivedHeartbeat()
}

func (n *Node) receivedHeartbeat() {
	n.lastSeen = time.Now()
	n.nextHeartBeat = time.Now().Add(ElectionTimeout/2 + time.Duration(rand.Intn(int(ElectionTimeout)/2)))
	if n.state == Candidate {
		n.convertToFollower()
	}
}

func (n *Node) AppendEntries(term Term, leaderId CandidateId, prevLogIndex Index, prevLogTerm Term, entries []*msg.Entry, leaderCommit Index) (Term, bool) {
	if term < n.list.GetCurrentTerm() {
		fmt.Println("Term before current term", term, n.list.GetCurrentTerm())
		return n.list.GetCurrentTerm(), false
	}
	nextIndex := n.list.GetNextIndex()
	if prevLogIndex >= 0 && nextIndex > 0 && nextIndex > prevLogIndex && n.list.Get(prevLogIndex).Term != prevLogTerm {
		fmt.Println("Not matching logs", prevLogIndex, nextIndex, prevLogTerm, n.list.Get(prevLogIndex))
		return n.list.GetCurrentTerm(), false
	}
	if n.list.GetCurrentTerm() < term {
		n.list.SetCurrentTerm(term)
		n.list.SetVotedFor(nil)
		if n.state != Follower {
			n.convertToFollower()
		}
	}
	n.receivedHeartbeat()
	n.leaderId = &leaderId
	offset := int(prevLogIndex)
	if len(entries) > 0 {
		indexOfLast := 0
		for i, v := range entries {
			if Index(i+offset+1) < nextIndex {
				if n.list.Get(Index(i+offset+1)).Term != Term(v.Term) {
					fmt.Println("Mismatched log")
					n.list.Truncate(Index(offset + i))
				}
			} else {
				indexOfLast = offset + i + 1
				n.list.Add(Entry{Index: Index(v.Index), Term: Term(v.Term), Value: v.Value})
			}
		}
		if leaderCommit > n.commitIndex && indexOfLast > 0 {
			n.commitIndex = min(leaderCommit, Index(indexOfLast))
		}
	} else {
		indexOfLast := n.getLastEntry().Index + 1
		if leaderCommit > n.commitIndex && indexOfLast > 0 {
			n.commitIndex = min(leaderCommit, Index(indexOfLast))
		}
	}
	return n.list.GetCurrentTerm(), true
}

func (n *Node) compareLog(lastLogIndex Index, lastLogTerm Term) bool {
	if n.list.GetNextIndex() >= lastLogIndex {
		return false
	}
	return n.list.Get(lastLogIndex).Term == lastLogTerm
}

func (n *Node) RequestVote(term Term, candidateId CandidateId, lastLogIndex Index, lastLogTerm Term) (Term, bool) {
	//fmt.Println("Got request vote")
	if n.list.GetCurrentTerm() < term && n.state != Follower {
		n.list.SetCurrentTerm(term)
		n.list.SetVotedFor(nil)
		n.convertToFollower()
		return n.list.GetCurrentTerm(), true
	} else if term == n.list.GetCurrentTerm() && (n.list.GetVotedFor() == nil || *n.list.GetVotedFor() == candidateId) && n.compareLog(lastLogIndex, lastLogTerm) {
		return n.list.GetCurrentTerm(), true
	}
	return n.list.GetCurrentTerm(), false
}
