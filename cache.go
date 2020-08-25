package disgordtlru

import (
	"container/list"
	"github.com/andersfylling/disgord"
	"github.com/andersfylling/disgord/json"
	"github.com/auttaja/go-tlru"
	"sync"
	"time"
)

type idHolder struct {
	ID      disgord.Snowflake `json:"id"`
	Channel struct {
		ID disgord.Snowflake `json:"id"`
	} `json:"channel"`
	Guild struct {
		ID disgord.Snowflake `json:"id"`
	} `json:"guild"`
	User struct {
		ID disgord.Snowflake `json:"id"`
	} `json:"user"`
	UserID    disgord.Snowflake `json:"user_id"`
	GuildID   disgord.Snowflake `json:"guild_id"`
	ChannelID disgord.Snowflake `json:"channel_id"`
}

// A wrapper of the TLRU cache with a mutex built in for ease of use.
// Note that whilst the TLRU already has a mutex built in, this is to stop internal purge race conditions rather than codebase ones like we want to solve here.
type tlruWrapper struct {
	*tlru.Cache
	sync.Mutex
}

// Defines the cache.
type cache struct {
	disgord.CacheNop

	ReturnGetGuildMembers bool

	CurrentUserMu sync.Mutex
	CurrentUser   *disgord.User

	ChannelMu                sync.RWMutex
	Channels                 map[disgord.Snowflake]*disgord.Channel
	GuildChannelRelationship map[disgord.Snowflake]*list.List

	Users       *tlruWrapper
	VoiceStates *tlruWrapper
	Guilds      *tlruWrapper
}

func (c *cache) registerChannelRelationship(guildId, channelId disgord.Snowflake) {
	if guildId == 0 {
		return
	}
	relationships, ok := c.GuildChannelRelationship[guildId]
	if !ok {
		relationships = list.New()
		c.GuildChannelRelationship[guildId] = relationships
	}
	relationships.PushBack(channelId)
}

func (c *cache) destroyChannelRelationship(guildId, channelId disgord.Snowflake) {
	if guildId == 0 {
		return
	}
	relationships, ok := c.GuildChannelRelationship[guildId]
	if !ok {
		return
	}
	blank := true
	for x := relationships.Front(); x != nil; x = x.Next() {
		if x.Value.(disgord.Snowflake) == channelId {
			relationships.Remove(x)
			break
		}
		blank = false
	}
	if blank {
		delete(c.GuildChannelRelationship, guildId)
	}
}

func (c *cache) Ready(data []byte) (*disgord.Ready, error) {
	c.CurrentUserMu.Lock()
	defer c.CurrentUserMu.Unlock()

	rdy := &disgord.Ready{
		User: c.CurrentUser,
	}

	err := json.Unmarshal(data, rdy)
	return rdy, err
}

func (c *cache) ChannelCreate(data []byte) (*disgord.ChannelCreate, error) {
	wrap := func(c *disgord.Channel) *disgord.ChannelCreate {
		return &disgord.ChannelCreate{Channel: c}
	}

	var channel *disgord.Channel
	if err := json.Unmarshal(data, &channel); err != nil {
		return nil, err
	}

	c.ChannelMu.Lock()
	defer c.ChannelMu.Unlock()
	if wrapper, exists := c.Channels[channel.ID]; exists {
		err := json.Unmarshal(data, wrapper)
		return wrap(channel), err
	}

	c.Channels[channel.ID] = channel
	c.registerChannelRelationship(channel.GuildID, channel.ID)

	return wrap(channel), nil
}

func (c *cache) ChannelUpdate(data []byte) (*disgord.ChannelUpdate, error) {
	var metadata *idHolder
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, err
	}
	channelID := metadata.ID

	c.ChannelMu.Lock()
	defer c.ChannelMu.Unlock()

	var channel *disgord.Channel
	var exists bool
	if channel, exists = c.Channels[channelID]; exists {
		if err := json.Unmarshal(data, channel); err != nil {
			return nil, err
		}
	} else {
		if err := json.Unmarshal(data, &channel); err != nil {
			return nil, err
		}
		c.Channels[channelID] = channel
		c.registerChannelRelationship(channel.GuildID, channel.ID)
	}

	return &disgord.ChannelUpdate{Channel: channel}, nil
}

func (c *cache) ChannelDelete(data []byte) (*disgord.ChannelDelete, error) {
	var cd *disgord.ChannelDelete
	if err := json.Unmarshal(data, &cd); err != nil {
		return nil, err
	}

	c.ChannelMu.Lock()
	defer c.ChannelMu.Unlock()
	delete(c.Channels, cd.Channel.ID)
	c.destroyChannelRelationship(cd.Channel.GuildID, cd.Channel.ID)

	return cd, nil
}

func (c *cache) ChannelPinsUpdate(data []byte) (*disgord.ChannelPinsUpdate, error) {
	var cpu *disgord.ChannelPinsUpdate
	if err := json.Unmarshal(data, &cpu); err != nil {
		return nil, err
	}

	if cpu.LastPinTimestamp.IsZero() {
		return cpu, nil
	}

	c.ChannelMu.Lock()
	defer c.ChannelMu.Unlock()
	if channel, exists := c.Channels[cpu.ChannelID]; exists {
		channel.LastPinTimestamp = cpu.LastPinTimestamp
	}

	return cpu, nil
}

func (c *cache) UserUpdate(data []byte) (*disgord.UserUpdate, error) {
	update := &disgord.UserUpdate{User: c.CurrentUser}

	c.CurrentUserMu.Lock()
	defer c.CurrentUserMu.Unlock()
	if err := json.Unmarshal(data, update); err != nil {
		return nil, err
	}

	return update, nil
}

func (c *cache) VoiceServerUpdate(data []byte) (*disgord.VoiceServerUpdate, error) {
	var vsu *disgord.VoiceServerUpdate
	if err := json.Unmarshal(data, &vsu); err != nil {
		return nil, err
	}

	return vsu, nil
}

func (c *cache) GuildMemberRemove(data []byte) (*disgord.GuildMemberRemove, error) {
	var gmr *disgord.GuildMemberRemove
	if err := json.Unmarshal(data, &gmr); err != nil {
		return nil, err
	}

	c.Guilds.Lock()
	defer c.Guilds.Unlock()

	if item, exists := c.Guilds.Get(gmr.GuildID); exists {
		guild := item.(*disgord.Guild)

		for i := range guild.Members {
			if guild.Members[i].UserID == gmr.User.ID {
				guild.MemberCount--
				guild.Members[i] = guild.Members[len(guild.Members)-1]
				guild.Members = guild.Members[:len(guild.Members)-1]
			}
		}
	}

	return gmr, nil
}

func (c *cache) GuildMemberAdd(data []byte) (*disgord.GuildMemberAdd, error) {
	var gmr *disgord.GuildMemberAdd
	if err := json.Unmarshal(data, &gmr); err != nil {
		return nil, err
	}

	userID := gmr.Member.User.ID
	c.Users.Lock()
	if _, exists := c.Users.Get(userID); !exists {
		c.Users.Set(userID, gmr.Member.User)
	}
	c.Users.Unlock()

	c.Guilds.Lock()
	defer c.Guilds.Unlock()

	if item, exists := c.Guilds.Get(gmr.Member.GuildID); exists {
		guild := item.(*disgord.Guild)

		var member *disgord.Member
		for i := range guild.Members { // slow... map instead?
			if guild.Members[i].UserID == gmr.Member.User.ID {
				member = guild.Members[i]
				if err := json.Unmarshal(data, member); err != nil {
					return nil, err
				}
				break
			}
		}
		if member == nil {
			member = &disgord.Member{}
			*member = *gmr.Member

			guild.Members = append(guild.Members, member)
			guild.MemberCount++
		}
		member.User = nil
	}

	return gmr, nil
}

func (c *cache) GuildCreate(data []byte) (*disgord.GuildCreate, error) {
	var guildEvt *disgord.GuildCreate
	if err := json.Unmarshal(data, &guildEvt); err != nil {
		return nil, err
	}

	c.Guilds.Lock()
	defer c.Guilds.Unlock()

	setChannels := func() {
		c.ChannelMu.Lock()
		defer c.ChannelMu.Unlock()
		relationships, ok := c.GuildChannelRelationship[guildEvt.Guild.ID]
		if ok {
			// We should remove these.
			for x := relationships.Front(); x != nil; x = x.Next() {
				delete(c.Channels, x.Value.(disgord.Snowflake))
			}
		}
		relationships = list.New()
		c.GuildChannelRelationship[guildEvt.Guild.ID] = relationships
		for _, channel := range guildEvt.Guild.Channels {
			relationships.PushBack(channel.ID)
			c.Channels[channel.ID] = channel.DeepCopy().(*disgord.Channel)
		}
	}

	if item, exists := c.Guilds.Get(guildEvt.Guild.ID); exists {
		guild := item.(*disgord.Guild)
		if !guild.Unavailable {
			if len(guild.Members) > 0 {
				// seems like an update event came before create
				// this kinda... isn't good
				_ = json.Unmarshal(data, item)
			} else {
				// duplicate event
				return guildEvt, nil
			}
		} else {
			c.Guilds.Set(guildEvt.Guild.ID, guildEvt.Guild)
			setChannels()
		}
	} else {
		c.Guilds.Set(guildEvt.Guild.ID, guildEvt.Guild)
		setChannels()
	}

	return guildEvt, nil
}

func (c *cache) GuildUpdate(data []byte) (*disgord.GuildUpdate, error) {
	var guildEvt *disgord.GuildUpdate
	if err := json.Unmarshal(data, &guildEvt); err != nil {
		return nil, err
	}

	c.Guilds.Lock()
	defer c.Guilds.Unlock()

	if item, exists := c.Guilds.Get(guildEvt.Guild.ID); exists {
		guild := item.(*disgord.Guild)
		if guild.Unavailable {
			c.Guilds.Set(guildEvt.Guild.ID, guildEvt.Guild)
		} else if err := json.Unmarshal(data, item); err != nil {
			return nil, err
		}
	} else {
		c.Guilds.Set(guildEvt.Guild.ID, guildEvt.Guild)
	}

	return guildEvt, nil
}

func (c *cache) GuildDelete(data []byte) (*disgord.GuildDelete, error) {
	var guildEvt *disgord.GuildDelete
	if err := json.Unmarshal(data, &guildEvt); err != nil {
		return nil, err
	}

	c.Guilds.Lock()
	defer c.Guilds.Unlock()
	c.Guilds.Delete(guildEvt.UnavailableGuild.ID)

	c.ChannelMu.Lock()
	defer c.ChannelMu.Unlock()
	relationships, ok := c.GuildChannelRelationship[guildEvt.UnavailableGuild.ID]
	if ok {
		for x := relationships.Front(); x != nil; x = x.Next() {
			delete(c.Channels, x.Value.(disgord.Snowflake))
		}
		delete(c.GuildChannelRelationship, guildEvt.UnavailableGuild.ID)
	}

	return guildEvt, nil
}

func (c *cache) GetChannel(id disgord.Snowflake) (*disgord.Channel, error) {
	c.ChannelMu.RLock()
	res, ok := c.Channels[id]
	if !ok {
		c.ChannelMu.RUnlock()
		return nil, nil
	}
	cpy := res.DeepCopy().(*disgord.Channel)
	c.ChannelMu.RUnlock()
	return cpy, nil
}

func (c *cache) GetGuildEmoji(guildID, emojiID disgord.Snowflake) (*disgord.Emoji, error) {
	c.Guilds.Lock()
	defer c.Guilds.Unlock()
	guild, ok := c.Guilds.Get(guildID)
	if !ok {
		return nil, nil
	}
	for _, emoji := range guild.(*disgord.Guild).Emojis {
		if emoji.ID == emojiID {
			return emoji.DeepCopy().(*disgord.Emoji), nil
		}
	}
	return nil, nil
}

func (c *cache) GetGuildEmojis(id disgord.Snowflake) ([]*disgord.Emoji, error) {
	c.Guilds.Lock()
	defer c.Guilds.Unlock()
	guild, ok := c.Guilds.Get(id)
	if !ok {
		return nil, nil
	}
	a := make([]*disgord.Emoji, len(guild.(*disgord.Guild).Emojis))
	for i, emoji := range guild.(*disgord.Guild).Emojis {
		a[i] = emoji.DeepCopy().(*disgord.Emoji)
	}
	return a, nil
}

func (c *cache) GetGuild(id disgord.Snowflake) (*disgord.Guild, error) {
	// Make a copy of the guild.
	c.Guilds.Lock()
	res, ok := c.Guilds.Get(id)
	if !ok {
		c.Guilds.Unlock()
		return nil, nil
	}
	var membersBefore []*disgord.Member
	if !c.ReturnGetGuildMembers {
		g := res.(*disgord.Guild)
		membersBefore = g.Members
		g.Members = []*disgord.Member{}
	}
	cpy := res.(*disgord.Guild).DeepCopy().(*disgord.Guild)
	if !c.ReturnGetGuildMembers {
		res.(*disgord.Guild).Members = membersBefore
	}
	c.Guilds.Unlock()

	// Get the channels.
	channelsRes, _ := c.GetGuildChannels(id)
	if channelsRes != nil {
		cpy.Channels = channelsRes
	}

	// Return the copy.
	return cpy, nil
}

func (c *cache) GetGuildChannels(id disgord.Snowflake) ([]*disgord.Channel, error) {
	c.ChannelMu.RLock()
	defer c.ChannelMu.RUnlock()
	relationships, ok := c.GuildChannelRelationship[id]
	if !ok {
		return nil, nil
	}
	channels := make([]*disgord.Channel, relationships.Len())
	i := 0
	for x := relationships.Front(); x != nil; x = x.Next() {
		channels[i] = c.Channels[x.Value.(disgord.Snowflake)].DeepCopy().(*disgord.Channel)
		i++
	}
	return channels, nil
}

func (c *cache) GetMember(guildID, userID disgord.Snowflake) (*disgord.Member, error) {
	c.Guilds.Lock()
	defer c.Guilds.Unlock()
	guild, ok := c.Guilds.Get(guildID)
	if !ok {
		return nil, nil
	}
	for _, member := range guild.(*disgord.Guild).Members {
		if member.UserID == userID {
			return member, nil
		}
	}
	return nil, nil
}

func (c *cache) GetGuildRoles(guildID disgord.Snowflake) ([]*disgord.Role, error) {
	c.Guilds.Lock()
	defer c.Guilds.Unlock()
	guild, ok := c.Guilds.Get(guildID)
	if !ok {
		return nil, nil
	}
	a := make([]*disgord.Role, len(guild.(*disgord.Guild).Emojis))
	for i, role := range guild.(*disgord.Guild).Roles {
		a[i] = role.DeepCopy().(*disgord.Role)
	}
	return a, nil
}

func (c *cache) GetCurrentUser() (*disgord.User, error) {
	c.CurrentUserMu.Lock()
	var cpy *disgord.User
	if c.CurrentUser != nil {
		cpy = c.CurrentUser.DeepCopy().(*disgord.User)
	}
	c.CurrentUserMu.Unlock()
	return cpy, nil
}

func (c *cache) GetUser(id disgord.Snowflake) (*disgord.User, error) {
	c.Users.Lock()
	res, ok := c.Users.Get(id)
	if !ok {
		c.Users.Unlock()
		return nil, nil
	}
	cpy := res.(*disgord.User).DeepCopy().(*disgord.User)
	c.Users.Unlock()
	return cpy, nil
}

// CacheConfig is used to define the cache configuration.
type CacheConfig struct {
	DoNotReturnGetGuildMembers bool

	UserMaxItems int
	UserMaxBytes int
	UserDuration time.Duration

	VoiceStatesMaxItems int
	VoiceStatesMaxBytes int
	VoiceStatesDuration time.Duration

	GuildMaxItems int
	GuildMaxBytes int
	GuildDuration time.Duration
}

// NewCache is used to create a new cache.
func NewCache(conf CacheConfig) disgord.Cache {
	return &cache{
		ReturnGetGuildMembers:    !conf.DoNotReturnGetGuildMembers,
		CurrentUser:              &disgord.User{},
		ChannelMu:                sync.RWMutex{},
		Channels:                 map[disgord.Snowflake]*disgord.Channel{},
		GuildChannelRelationship: map[disgord.Snowflake]*list.List{},
		Users:                    &tlruWrapper{Cache: tlru.NewCache(conf.UserMaxItems, conf.UserMaxBytes, conf.UserDuration)},
		VoiceStates:              &tlruWrapper{Cache: tlru.NewCache(conf.VoiceStatesMaxItems, conf.VoiceStatesMaxBytes, conf.VoiceStatesDuration)},
		Guilds:                   &tlruWrapper{Cache: tlru.NewCache(conf.GuildMaxItems, conf.GuildMaxBytes, conf.GuildDuration)},
	}
}
