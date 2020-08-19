package disgordtlru

import (
	"github.com/andersfylling/disgord"
	"github.com/jakemakesstuff/go-tlru"
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

	unmarshalUpdate disgord.UnmarshalUpdater

	CurrentUserMu sync.Mutex
	CurrentUser   *disgord.User

	Users       *tlruWrapper
	VoiceStates *tlruWrapper
	Channels    *tlruWrapper
	Guilds     	*tlruWrapper
}

func (c *cache) RegisterUnmarshaler(unmarshaler disgord.UnmarshalUpdater) {
	c.unmarshalUpdate = unmarshaler
	c.CacheNop.RegisterUnmarshaler(unmarshaler)
}

func (c *cache) Ready(data []byte) (*disgord.Ready, error) {
	c.CurrentUserMu.Lock()
	defer c.CurrentUserMu.Unlock()

	rdy := &disgord.Ready{
		User: c.CurrentUser,
	}

	err := c.unmarshalUpdate(data, rdy)
	return rdy, err
}

func (c *cache) ChannelCreate(data []byte) (*disgord.ChannelCreate, error) {
	wrap := func(c *disgord.Channel) *disgord.ChannelCreate {
		return &disgord.ChannelCreate{Channel: c}
	}

	var channel *disgord.Channel
	if err := c.unmarshalUpdate(data, &channel); err != nil {
		return nil, err
	}

	c.Channels.Lock()
	defer c.Channels.Unlock()
	if wrapper, exists := c.Channels.Get(channel.ID); exists {
		err := c.unmarshalUpdate(data, wrapper)
		return wrap(channel), err
	}

	c.Channels.Set(channel.ID, channel)

	return wrap(channel), nil
}

func (c *cache) ChannelUpdate(data []byte) (*disgord.ChannelUpdate, error) {
	var metadata *idHolder
	if err := c.unmarshalUpdate(data, &metadata); err != nil {
		return nil, err
	}
	channelID := metadata.ID

	c.Channels.Lock()
	defer c.Channels.Unlock()

	var channel *disgord.Channel
	if item, exists := c.Channels.Get(channelID); exists {
		channel = item.(*disgord.Channel)
		if err := c.unmarshalUpdate(data, channel); err != nil {
			return nil, err
		}
	} else {
		if err := c.unmarshalUpdate(data, &channel); err != nil {
			return nil, err
		}
		c.Channels.Set(channelID, item)
	}

	return &disgord.ChannelUpdate{Channel: channel}, nil
}

func (c *cache) ChannelDelete(data []byte) (*disgord.ChannelDelete, error) {
	var cd *disgord.ChannelDelete
	if err := c.unmarshalUpdate(data, &cd); err != nil {
		return nil, err
	}

	c.Channels.Lock()
	defer c.Channels.Unlock()
	c.Channels.Delete(cd.Channel.ID)

	return cd, nil
}

func (c *cache) ChannelPinsUpdate(data []byte) (*disgord.ChannelPinsUpdate, error) {
	var cpu *disgord.ChannelPinsUpdate
	if err := c.unmarshalUpdate(data, &cpu); err != nil {
		return nil, err
	}

	if cpu.LastPinTimestamp.IsZero() {
		return cpu, nil
	}

	c.Channels.Lock()
	defer c.Channels.Unlock()
	if item, exists := c.Channels.Get(cpu.ChannelID); exists {
		channel := item.(*disgord.Channel)
		channel.LastPinTimestamp = cpu.LastPinTimestamp
	}

	return cpu, nil
}

func (c *cache) UserUpdate(data []byte) (*disgord.UserUpdate, error) {
	update := &disgord.UserUpdate{User: c.CurrentUser}

	c.CurrentUserMu.Lock()
	defer c.CurrentUserMu.Unlock()
	if err := c.unmarshalUpdate(data, update); err != nil {
		return nil, err
	}

	return update, nil
}

func (c *cache) VoiceServerUpdate(data []byte) (*disgord.VoiceServerUpdate, error) {
	var vsu *disgord.VoiceServerUpdate
	if err := c.unmarshalUpdate(data, &vsu); err != nil {
		return nil, err
	}

	return vsu, nil
}

func (c *cache) GuildMemberRemove(data []byte) (*disgord.GuildMemberRemove, error) {
	var gmr *disgord.GuildMemberRemove
	if err := c.unmarshalUpdate(data, &gmr); err != nil {
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
	if err := c.unmarshalUpdate(data, &gmr); err != nil {
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
				if err := c.unmarshalUpdate(data, member); err != nil {
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
	if err := c.unmarshalUpdate(data, &guildEvt); err != nil {
		return nil, err
	}

	c.Guilds.Lock()
	defer c.Guilds.Unlock()

	if item, exists := c.Guilds.Get(guildEvt.Guild.ID); exists {
		guild := item.(*disgord.Guild)
		if !guild.Unavailable {
			if len(guild.Members) > 0 {
				// seems like an update event came before create
				// this kinda... isn't good
				_ = c.unmarshalUpdate(data, item)
			} else {
				// duplicate event
				return guildEvt, nil
			}
		} else {
			c.Guilds.Set(guildEvt.Guild.ID, guildEvt.Guild)
		}
	} else {
		c.Guilds.Set(guildEvt.Guild.ID, guildEvt.Guild)
	}

	return guildEvt, nil
}

func (c *cache) GuildUpdate(data []byte) (*disgord.GuildUpdate, error) {
	var guildEvt *disgord.GuildUpdate
	if err := c.unmarshalUpdate(data, &guildEvt); err != nil {
		return nil, err
	}

	c.Guilds.Lock()
	defer c.Guilds.Unlock()

	if item, exists := c.Guilds.Get(guildEvt.Guild.ID); exists {
		guild := item.(*disgord.Guild)
		if guild.Unavailable {
			c.Guilds.Set(guildEvt.Guild.ID, guildEvt.Guild)
		} else if err := c.unmarshalUpdate(data, item); err != nil {
			return nil, err
		}
	} else {
		c.Guilds.Set(guildEvt.Guild.ID, guildEvt.Guild)
	}

	return guildEvt, nil
}

func (c *cache) GuildDelete(data []byte) (*disgord.GuildDelete, error) {
	var guildEvt *disgord.GuildDelete
	if err := c.unmarshalUpdate(data, &guildEvt); err != nil {
		return nil, err
	}

	c.Guilds.Lock()
	defer c.Guilds.Unlock()
	c.Guilds.Delete(guildEvt.UnavailableGuild.ID)

	return guildEvt, nil
}

// CacheConfig is used to define the cache configuration.
type CacheConfig struct {
	UserMaxItems int
	UserMaxBytes int
	UserDuration time.Duration

	VoiceStatesMaxItems int
	VoiceStatesMaxBytes int
	VoiceStatesDuration time.Duration

	ChannelMaxItems int
	ChannelMaxBytes int
	ChannelDuration time.Duration

	GuildMaxItems int
	GuildMaxBytes int
	GuildDuration time.Duration
}

// NewCache is used to create a new cache.
func NewCache(conf CacheConfig) disgord.Cache {
	return &cache{
		CurrentUser:     &disgord.User{},
		Users:           &tlruWrapper{Cache: tlru.NewCache(conf.UserMaxItems, conf.UserMaxBytes, conf.UserDuration)},
		VoiceStates:     &tlruWrapper{Cache: tlru.NewCache(conf.VoiceStatesMaxItems, conf.VoiceStatesMaxBytes, conf.VoiceStatesDuration)},
		Channels:        &tlruWrapper{Cache: tlru.NewCache(conf.ChannelMaxItems, conf.ChannelMaxBytes, conf.ChannelDuration)},
		Guilds:          &tlruWrapper{Cache: tlru.NewCache(conf.GuildMaxItems, conf.GuildMaxBytes, conf.GuildDuration)},
	}
}
