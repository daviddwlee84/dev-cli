//go:build linux

package sshcredential

import (
	"context"
	"github.com/godbus/dbus/v5"
)

type nativeSystemProvider struct{}

func (nativeSystemProvider) ID() string { return "system" }

const secretService = "org.freedesktop.secrets"
const secretRoot = dbus.ObjectPath("/org/freedesktop/secrets")

type serviceSecret struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

func secretConnection(ctx context.Context) (*dbus.Conn, dbus.ObjectPath, error) {
	conn, e := dbus.ConnectSessionBus()
	if e != nil {
		return nil, "", ErrUnavailable
	}
	var output dbus.Variant
	var session dbus.ObjectPath
	e = conn.Object(secretService, secretRoot).CallWithContext(ctx, "org.freedesktop.Secret.Service.OpenSession", 0, "plain", dbus.MakeVariant("")).Store(&output, &session)
	if e != nil {
		conn.Close()
		return nil, "", ErrUnavailable
	}
	return conn, session, nil
}
func (nativeSystemProvider) Available(ctx context.Context) error {
	conn, session, e := secretConnection(ctx)
	if e != nil {
		return e
	}
	defer conn.Close()
	_ = conn.Object(secretService, session).CallWithContext(ctx, "org.freedesktop.Secret.Session.Close", 0).Err
	return nil
}
func findSecret(ctx context.Context, conn *dbus.Conn, id string) (dbus.ObjectPath, error) {
	var unlocked, locked []dbus.ObjectPath
	e := conn.Object(secretService, secretRoot).CallWithContext(ctx, "org.freedesktop.Secret.Service.SearchItems", 0, map[string]string{"dev-owner": providerOwner, "dev-context": id}).Store(&unlocked, &locked)
	if e != nil {
		return "", ErrUnavailable
	}
	if len(locked) > 0 {
		return "", ErrLocked
	}
	if len(unlocked) == 0 {
		return "", ErrNotFound
	}
	if len(unlocked) != 1 {
		return "", ErrUnsafe
	}
	return unlocked[0], nil
}
func (p nativeSystemProvider) Get(ctx context.Context, c Context, ref Reference) ([]byte, error) {
	if validateNativeReference(c, ref) != nil {
		return nil, ErrUnsafe
	}
	conn, session, e := secretConnection(ctx)
	if e != nil {
		return nil, e
	}
	defer conn.Close()
	defer conn.Object(secretService, session).CallWithContext(context.Background(), "org.freedesktop.Secret.Session.Close", 0)
	item, e := findSecret(ctx, conn, c.ID())
	if e != nil {
		return nil, e
	}
	var secret serviceSecret
	if conn.Object(secretService, item).CallWithContext(ctx, "org.freedesktop.Secret.Item.GetSecret", 0, session).Store(&secret) != nil {
		return nil, ErrLocked
	}
	if validateSecret(secret.Value) != nil {
		Wipe(secret.Value)
		return nil, ErrUnsafe
	}
	return secret.Value, nil
}
func (p nativeSystemProvider) Put(ctx context.Context, c Context, old *Reference, secret []byte) (Reference, error) {
	if c.Validate() != nil || validateSecret(secret) != nil || old != nil && validateNativeReference(c, *old) != nil {
		return Reference{}, ErrUnsafe
	}
	conn, session, e := secretConnection(ctx)
	if e != nil {
		return Reference{}, e
	}
	defer conn.Close()
	defer conn.Object(secretService, session).CallWithContext(context.Background(), "org.freedesktop.Secret.Session.Close", 0)
	item, e := findSecret(ctx, conn, c.ID())
	if e == nil {
		if old == nil {
			return Reference{}, ErrUnsafe
		}
		if conn.Object(secretService, item).CallWithContext(ctx, "org.freedesktop.Secret.Item.SetSecret", 0, serviceSecret{session, nil, secret, "text/plain; charset=utf8"}).Err != nil {
			return Reference{}, ErrUnknown
		}
		return Reference{p.ID(), c.ID()}, nil
	}
	if e != ErrNotFound || old != nil {
		return Reference{}, e
	}
	var collection dbus.ObjectPath
	if conn.Object(secretService, secretRoot).CallWithContext(ctx, "org.freedesktop.Secret.Service.ReadAlias", 0, "default").Store(&collection) != nil || collection == "/" {
		return Reference{}, ErrUnavailable
	}
	var created, prompt dbus.ObjectPath
	props := map[string]dbus.Variant{"org.freedesktop.Secret.Item.Label": dbus.MakeVariant("dev SSH " + c.ID()[:16]), "org.freedesktop.Secret.Item.Attributes": dbus.MakeVariant(map[string]string{"dev-owner": providerOwner, "dev-context": c.ID()})}
	e = conn.Object(secretService, collection).CallWithContext(ctx, "org.freedesktop.Secret.Collection.CreateItem", 0, props, serviceSecret{session, nil, secret, "text/plain; charset=utf8"}, false).Store(&created, &prompt)
	if e != nil || prompt != "/" || created == "/" {
		return Reference{}, ErrUnknown
	}
	return Reference{p.ID(), c.ID()}, nil
}
func (p nativeSystemProvider) Delete(ctx context.Context, c Context, ref Reference) error {
	if validateNativeReference(c, ref) != nil {
		return ErrUnsafe
	}
	conn, session, e := secretConnection(ctx)
	if e != nil {
		return e
	}
	defer conn.Close()
	defer conn.Object(secretService, session).CallWithContext(context.Background(), "org.freedesktop.Secret.Session.Close", 0)
	item, e := findSecret(ctx, conn, c.ID())
	if e != nil {
		return e
	}
	var prompt dbus.ObjectPath
	if conn.Object(secretService, item).CallWithContext(ctx, "org.freedesktop.Secret.Item.Delete", 0).Store(&prompt) != nil || prompt != "/" {
		return ErrUnknown
	}
	return nil
}
