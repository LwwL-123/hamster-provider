package listener

import (
	ctx2 "context"
	"fmt"
	gsrpc "github.com/centrifuge/go-substrate-rpc-client/v4"
	"github.com/centrifuge/go-substrate-rpc-client/v4/types"
	chain2 "github.com/hamster-shared/hamster-provider/core/modules/chain"
	"github.com/hamster-shared/hamster-provider/core/modules/config"
	"github.com/hamster-shared/hamster-provider/core/modules/event"
	"github.com/hamster-shared/hamster-provider/core/modules/utils"
	log "github.com/sirupsen/logrus"
	"time"
)

type ChainListener struct {
	eventService event.IEventService
	api          *gsrpc.SubstrateAPI
	cm           *config.ConfigManager
	reportClient chain2.ReportClient
	cancel       func()
	ctx2         ctx2.Context
}

func NewChainListener(eventService event.IEventService, api *gsrpc.SubstrateAPI, cm *config.ConfigManager, reportClient chain2.ReportClient) *ChainListener {
	return &ChainListener{
		eventService: eventService,
		api:          api,
		cm:           cm,
		reportClient: reportClient,
	}
}

func (l *ChainListener) GetState() bool {
	return l.cancel != nil
}

func (l *ChainListener) SetState(option bool) error {
	if option {
		return l.start()
	} else {
		return l.stop()
	}
}

func (l *ChainListener) start() error {
	if l.cancel != nil {
		l.cancel()
	}

	cfg, err := l.cm.GetConfig()
	if err != nil {
		return err
	}
	resource := chain2.ResourceInfo{
		PeerId:     cfg.Identity.PeerID,
		Cpu:        cfg.Vm.Cpu,
		Memory:     cfg.Vm.Mem,
		System:     cfg.Vm.System,
		CpuModel:   utils.GetCpuModel(),
		Price:      cfg.ChainRegInfo.Price,
		ExpireTime: time.Now().AddDate(0, 0, 10),
	}
	err = l.reportClient.RegisterResource(resource)

	if err != nil {
		return err
	}

	l.ctx2, l.cancel = ctx2.WithCancel(ctx2.Background())
	go l.watchEvent(l.ctx2)
	return nil
}

func (l *ChainListener) stop() error {
	if l.cancel != nil {
		l.cancel()
		l.cancel = nil
	}
	cfg, err := l.cm.GetConfig()
	if err != nil {
		return err
	}
	return l.reportClient.RemoveResource(cfg.ChainRegInfo.ResourceIndex)
}

// WatchEvent chain event listener
func (l *ChainListener) watchEvent(ctx ctx2.Context) {

	meta, err := l.api.RPC.State.GetMetadataLatest()
	if err != nil {
		panic(err)
	}

	// Subscribe to system events via storage
	key, err := types.CreateStorageKey(meta, "System", "Events", nil)
	if err != nil {
		panic(err)
	}

	sub, err := l.api.RPC.State.SubscribeStorageRaw([]types.StorageKey{key})
	if err != nil {
		panic(err)
	}
	defer sub.Unsubscribe()

	for {
		select {
		case <-ctx.Done():
			return
		case set := <-sub.Chan():
			fmt.Println("监听链区块：", set.Block.Hex())
			for _, chng := range set.Changes {
				if !types.Eq(chng.StorageKey, key) || !chng.HasStorageData {
					// skip, we are only interested in events with content
					continue
				}
				// Decode the event records
				evt := chain2.MyEventRecords{}
				storageData := chng.StorageData
				meta, err := l.api.RPC.State.GetMetadataLatest()
				err = types.EventRecordsRaw(storageData).DecodeEventRecords(meta, &evt)
				if err != nil {
					fmt.Println(err)
					log.Error(err)
					continue
				}
				for _, e := range evt.ResourceOrder_CreateOrderSuccess {
					// order successfully created
					l.dealCreateOrderSuccess(e)
				}

				for _, e := range evt.ResourceOrder_ReNewOrderSuccess {
					// order renewal successful
					l.dealReNewOrderSuccess(e)
				}

				for _, e := range evt.ResourceOrder_WithdrawLockedOrderPriceSuccess {
					// order cancelled successfully
					l.dealCancelOrderSuccess(e)
				}
			}
		}
	}

}

func (l *ChainListener) dealCreateOrderSuccess(e chain2.EventResourceOrderCreateOrderSuccess) {
	cfg, err := l.cm.GetConfig()
	if err != nil {
		panic(err)
	}
	fmt.Printf("\tResourceOrder:CreateOrderSuccess:: (phase=%#v)\n", e.Phase)

	if e.ResourceIndex == types.NewU64(cfg.ChainRegInfo.ResourceIndex) {
		// process the order
		fmt.Println("deal order", e.OrderIndex)
		// record the id of the processed order
		cfg.ChainRegInfo.OrderIndex = uint64(e.OrderIndex)
		_ = l.cm.Save(cfg)
		evt := &event.VmRequest{
			Tag:       event.OPCreatedVm,
			Cpu:       cfg.Vm.Cpu,
			Mem:       cfg.Vm.Mem,
			Disk:      cfg.Vm.Disk,
			OrderNo:   uint64(e.OrderIndex),
			System:    cfg.Vm.System,
			PublicKey: e.PublicKey,
			Image:     cfg.Vm.Image,
		}
		l.eventService.Create(evt)

	} else {
		fmt.Println("resourceIndex is not equals ")
	}
}

func (l *ChainListener) dealReNewOrderSuccess(e chain2.EventResourceOrderReNewOrderSuccess) {
	cfg, err := l.cm.GetConfig()
	if err != nil {
		panic(err)
	}
	if e.ResourceIndex == types.NewU64(cfg.ChainRegInfo.ResourceIndex) {
		evt := &event.VmRequest{
			Tag:     event.OPRenewVM,
			OrderNo: uint64(e.OrderIndex),
		}
		l.eventService.Renew(evt)
	}
}

func (l *ChainListener) dealCancelOrderSuccess(e chain2.EventResourceOrderWithdrawLockedOrderPriceSuccess) {
	cfg, err := l.cm.GetConfig()
	if err != nil {
		panic(err)
	}
	if e.OrderIndex == types.NewU64(cfg.ChainRegInfo.OrderIndex) {
		evt := &event.VmRequest{
			Tag:     event.OPDestroyVm,
			Cpu:     cfg.Vm.Cpu,
			Mem:     cfg.Vm.Mem,
			Disk:    cfg.Vm.Disk,
			OrderNo: uint64(e.OrderIndex),
			System:  cfg.Vm.System,
			Image:   cfg.Vm.Image,
		}
		l.eventService.Destroy(evt)
	}
}
