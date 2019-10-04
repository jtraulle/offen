package router

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
	"github.com/offen/offen/server/persistence"
)

type inboundEventPayload struct {
	AccountID string `json:"accountId"`
	Payload   string `json:"payload"`
}

type ackResponse struct {
	Ack bool `json:"ack"`
}

var errBadRequestContext = errors.New("could not use user id in request context")

func (rt *router) postEvents(c *gin.Context) {
	userID, _ := c.Value(contextKeyCookie).(string)
	evt := inboundEventPayload{}
	if err := c.BindJSON(&evt); err != nil {
		newJSONError(
			fmt.Errorf("router: error decoding request payload: %v", err),
			http.StatusBadRequest,
		).Respond(c)
		return
	}

	if err := rt.db.Insert(userID, evt.AccountID, evt.Payload); err != nil {
		if unknownAccountErr, ok := err.(persistence.ErrUnknownAccount); ok {
			newJSONError(
				unknownAccountErr,
				http.StatusNotFound,
			).Respond(c)
			return
		}
		if unknownUserErr, ok := err.(persistence.ErrUnknownUser); ok {
			newJSONError(
				unknownUserErr,
				http.StatusBadRequest,
			).Respond(c)
			return
		}
		newJSONError(
			fmt.Errorf("router: error persisting event: %v", err),
			http.StatusInternalServerError,
		).Respond(c)
		return
	}
	// this handler might be called without a cookie / i.e. receiving an
	// anonymous event, in which case it is important **NOT** to re-issue
	// the user cookie.
	if userID != "" {
		http.SetCookie(c.Writer, rt.userCookie(userID))
	}
	c.JSON(http.StatusCreated, ackResponse{true})
}

type getQuery struct {
	params url.Values
	userID string
}

func (q *getQuery) AccountIDs() []string {
	return q.params["accountId"]
}

func (q *getQuery) UserID() string {
	return q.userID
}

func (q *getQuery) Since() string {
	return q.params.Get("since")
}

type getResponse struct {
	Events map[string][]persistence.EventResult `json:"events"`
}

func (rt *router) getEvents(c *gin.Context) {
	userID, ok := c.Value(contextKeyCookie).(string)
	if !ok {
		newJSONError(
			errBadRequestContext,
			http.StatusInternalServerError,
		).Respond(c)
		return
	}
	query := getQuery{
		params: c.Request.URL.Query(),
		userID: userID,
	}

	result, err := rt.db.Query(&query)
	if err != nil {
		newJSONError(
			fmt.Errorf("router: error performing event query: %v", err),
			http.StatusInternalServerError,
		).Respond(c)
		return
	}
	// the query result gets wrapped in a top level object before marshalling
	// it into JSON so new data can easily be added or removed
	outbound := getResponse{
		Events: result,
	}
	c.JSON(http.StatusOK, outbound)
}

type deletedQuery struct {
	EventIDs []string `json:"eventIds"`
}

func (rt *router) getDeletedEvents(c *gin.Context) {
	userID, _ := c.Value(contextKeyCookie).(string)

	query := deletedQuery{}
	if err := c.BindJSON(&query); err != nil {
		newJSONError(
			fmt.Errorf("router: error decoding request payload: %v", err),
			http.StatusBadRequest,
		).Respond(c)
		return
	}
	deleted, err := rt.db.GetDeletedEvents(query.EventIDs, userID)
	if err != nil {
		newJSONError(
			fmt.Errorf("router: error getting deleted events: %v", err),
			http.StatusInternalServerError,
		).Respond(c)
		return
	}
	out := deletedQuery{
		EventIDs: deleted,
	}
	c.JSON(http.StatusOK, out)
}

func (rt *router) purgeEvents(c *gin.Context) {
	userID, _ := c.Value(contextKeyCookie).(string)
	if err := rt.db.Purge(userID); err != nil {
		newJSONError(
			fmt.Errorf("router: error purging user events: %v", err),
			http.StatusInternalServerError,
		).Respond(c)
		return
	}
	c.Status(http.StatusNoContent)
}
