package syncer

import (
	"context"
	"log/slog"
	"time"

	"kandji-cloudflare-device-sync/cloudflare"
	"kandji-cloudflare-device-sync/config"
	"kandji-cloudflare-device-sync/kandji"
)

// Syncer orchestrates the synchronization from Kandji to Cloudflare.
type Syncer struct {
	kandjiClient     *kandji.Client
	cloudflareClient *cloudflare.Client
	config           *config.Config
	log              *slog.Logger
}

// New creates a new Syncer.
func New(kClient *kandji.Client, cClient *cloudflare.Client, cfg *config.Config, log *slog.Logger) *Syncer {
	return &Syncer{
		kandjiClient:     kClient,
		cloudflareClient: cClient,
		config:           cfg,
		log:              log,
	}
}

// Run starts the synchronization loop, running at the specified interval.
func (s *Syncer) Run(ctx context.Context, syncInterval time.Duration) {
	s.log.Info("Starting sync process",
		"interval", syncInterval.String(),
		"on_missing", s.config.OnMissing,
		"sync_devices_without_owners", s.config.Kandji.SyncDevicesWithoutOwners,
		"sync_mobile_devices", s.config.Kandji.SyncMobileDevices,
		"include_tags", s.config.Kandji.IncludeTags,
		"exclude_tags", s.config.Kandji.ExcludeTags,
		"blueprints_include", s.config.Kandji.BlueprintsInclude,
		"blueprints_exclude", s.config.Kandji.BlueprintsExclude)

	ticker := time.NewTicker(syncInterval)
	defer ticker.Stop()

	// Run a sync immediately on start-up
	s.Sync(ctx)

	for {
		select {
		case <-ticker.C:
			s.Sync(ctx)
		case <-ctx.Done():
			s.log.Info("Sync process stopping due to context cancellation.")
			return
		}
	}
}

// Sync performs a single synchronization cycle.
func (s *Syncer) Sync(ctx context.Context) {
	s.log.Info("Starting new sync cycle")

	// 1. Get devices from Kandji and filter
	kandjiDevices, err := s.kandjiClient.GetDevices(ctx)
	if err != nil {
		s.log.Error("Failed to get devices from Kandji", "error", err)
		return
	}
	s.log.Debug("Successfully fetched devices from Kandji", "count", len(kandjiDevices))

	var filteredKandjiSerials []string
	var filteredKandjiDevices []kandji.Device
	for _, device := range kandjiDevices {
		if device.SerialNumber == "" {
			s.log.Debug("Skipping device with empty serial number", "device_name", device.DeviceName)
			continue
		}
		if !s.config.Kandji.SyncDevicesWithoutOwners && device.UserEmail == "" {
			s.log.Debug("Skipping device without owner", "serial_number", device.SerialNumber)
			continue
		}
		if !s.config.Kandji.SyncMobileDevices && (device.Platform == "iPhone" || device.Platform == "iPad") {
			s.log.Debug("Skipping mobile device", "serial_number", device.SerialNumber)
			continue
		}
		if len(s.config.Kandji.IncludeTags) > 0 && !s.deviceHasAnyTag(device, s.config.Kandji.IncludeTags) {
			continue
		}
		if len(s.config.Kandji.ExcludeTags) > 0 && s.deviceHasAnyTag(device, s.config.Kandji.ExcludeTags) {
			continue
		}

		// Blueprint filtering
		if !s.deviceMatchesBlueprint(&device) {
			continue
		}

		filteredKandjiDevices = append(filteredKandjiDevices, device)
		s.log.Debug("Including device for sync", "serial_number", device.SerialNumber)
	}
	s.log.Info("Total new devices in Kandji that pass filters", "count", len(filteredKandjiDevices))

	// 2. Fetch serials from all source Cloudflare lists
	mergedSourceSerials := createSet(filteredKandjiSerials)

	sourceListDescriptions := make(map[string]string) // listID -> description

	for _, sourceListID := range s.config.Cloudflare.SourceListIDs {
		listType, err := s.cloudflareClient.GetListTypeByID(ctx, sourceListID)
		if err != nil {
			s.log.Error("Failed to fetch type for source Cloudflare list", "list_id", sourceListID, "error", err)
			continue
		}
		targetType, err := s.cloudflareClient.GetListTypeByID(ctx, s.config.Cloudflare.ListID)
		if err != nil {
			s.log.Error("Failed to fetch type for target Cloudflare list", "list_id", s.config.Cloudflare.ListID, "error", err)
			continue
		}
		if listType != targetType {
			s.log.Error("Source list type does not match target list type", "source_list_id", sourceListID, "source_type", listType, "target_type", targetType)
			continue
		}

		// Fetch the source list metadata to get its description
		sourceListMeta, err := s.cloudflareClient.GetListMetadataByID(ctx, sourceListID)
		if err == nil && sourceListMeta.Description != "" {
			sourceListDescriptions[sourceListID] = sourceListMeta.Description
		}

		items, err := s.cloudflareClient.GetListItemsByID(ctx, sourceListID)
		if err != nil {
			s.log.Error("Failed to fetch items from source Cloudflare list", "list_id", sourceListID, "error", err)
			continue
		}
		for _, item := range items {
			mergedSourceSerials[item.Value] = struct{}{}
		}
		s.log.Info("Merged serials from source Cloudflare list", "list_id", sourceListID, "count", len(items))
	}

	// 3. Fetch current serials from target Cloudflare list
	targetSerials, err := s.cloudflareClient.GetListItems(ctx)
	if err != nil {
		s.log.Error("Failed to get devices from Cloudflare target list", "error", err)
		return
	}
	targetSerialSet := make(map[string]struct{}, len(targetSerials))
	for _, serial := range targetSerials {
		targetSerialSet[serial] = struct{}{}
	}
	s.log.Debug("Fetched serials from target Cloudflare list", "count", len(targetSerials))

	// 4. Remove any devices from the target list that are not in the merged set (if on_missing == "delete")
	var toRemove []string
	if s.config.OnMissing == "delete" {
		for serial := range targetSerialSet {
			if _, keep := mergedSourceSerials[serial]; !keep {
				toRemove = append(toRemove, serial)
			}
		}
		if len(toRemove) > 0 {
			s.log.Info("Deleting devices in target Cloudflare list that are not present in merged sources", "count", len(toRemove), "batch_size", s.config.Batch.Size)
			result, err := s.cloudflareClient.DeleteDevices(ctx, toRemove, s.config.Batch.Size)
			if err != nil {
				s.log.Error("Failed to delete missing devices", "error", err)
				return
			}
			s.log.Info("Bulk device deletion completed", "success_count", result.SuccessCount, "failed_count", len(result.FailedDevices), "error_count", len(result.Errors))
			for _, failedDevice := range result.FailedDevices {
				s.log.Error("Failed to delete device", "serial_number", failedDevice.SerialNumber, "error", failedDevice.Error)
			}
			for _, generalError := range result.Errors {
				s.log.Error("Bulk deletion error", "error", generalError)
			}
		}
	}

	// 5. Push any new devices to target list
	type deviceWithComment struct {
		SerialNumber string
		Comment      string
	}

	var toAdd []deviceWithComment
	for _, device := range filteredKandjiDevices {
		if _, exists := targetSerialSet[device.SerialNumber]; !exists {
			toAdd = append(toAdd, deviceWithComment{
				SerialNumber: device.SerialNumber,
				Comment:      device.DeviceName,
			})
		}
	}

	/*
	   Optimization: Avoid repeated API calls for source lists by caching items.
	*/
	sourceListItemsCache := make(map[string][]cloudflare.GatewayListItem)
	for _, sourceListID := range s.config.Cloudflare.SourceListIDs {
		items, err := s.cloudflareClient.GetListItemsByID(ctx, sourceListID)
		if err == nil {
			sourceListItemsCache[sourceListID] = items
		}
	}

	// For source lists, add serials with the source list description as comment
	for _, sourceListID := range s.config.Cloudflare.SourceListIDs {
		items := sourceListItemsCache[sourceListID]
		desc := sourceListDescriptions[sourceListID]
		for _, item := range items {
			if _, exists := targetSerialSet[item.Value]; !exists {
				toAdd = append(toAdd, deviceWithComment{
					SerialNumber: item.Value,
					Comment:      desc,
				})
			}
		}
	}

	s.log.Info("Total new devices to add to target Cloudflare list", "count", len(toAdd))

	if len(toAdd) > 0 {
		// Defensive deduplication: filter out any serials already in the target list
		deduped := make([]deviceWithComment, 0, len(toAdd))
		intersection := make([]string, 0)
		for _, d := range toAdd {
			if _, exists := targetSerialSet[d.SerialNumber]; !exists {
				deduped = append(deduped, d)
			} else {
				intersection = append(intersection, d.SerialNumber)
			}
		}
		if len(intersection) > 0 {
			s.log.Warn("Deduplication: serials to be appended already exist in target list", "count", len(intersection), "serials", intersection)
		}
		if len(deduped) == 0 {
			s.log.Info("No new devices to add after deduplication")
			return
		}

		var cfDevices []cloudflare.GatewayListItemCreateRequest
		serialSeen := make(map[string]struct{})
		duplicates := make([]string, 0)
		for _, d := range deduped {
			if _, exists := serialSeen[d.SerialNumber]; exists {
				duplicates = append(duplicates, d.SerialNumber)
				continue
			}
			serialSeen[d.SerialNumber] = struct{}{}
			cfDevices = append(cfDevices, cloudflare.GatewayListItemCreateRequest{
				Value:   d.SerialNumber,
				Comment: d.Comment,
			})
		}
		if len(duplicates) > 0 {
			s.log.Warn("Deduplication: duplicate serials skipped in PATCH payload", "count", len(duplicates), "serials", duplicates)
		}

		s.log.Debug("PATCH append payload", "count", len(cfDevices), "serials", cfDevices)
		err := s.cloudflareClient.AppendDevices(ctx, cfDevices, s.config.Batch.Size)
		if err != nil {
			s.log.Error("Failed to process device batch", "error", err)
			return
		}
		s.log.Info("Bulk device creation completed", "success_count", len(cfDevices))
	}

	s.log.Info("Sync cycle complete",
		"kandji_devices_total", len(kandjiDevices),
		"eligible_devices", len(filteredKandjiDevices),
		"new_devices_found", len(toAdd),
		"successfully_added", len(toAdd),
		"deleted_devices", len(toRemove))
}

// createSet creates a set from a slice of strings for efficient lookups.
func createSet(items []string) map[string]struct{} {
	set := make(map[string]struct{}, len(items))
	for _, item := range items {
		set[item] = struct{}{}
	}
	return set
}

// deviceMatchesBlueprint checks if a device matches the blueprint filters.
func (s *Syncer) deviceMatchesBlueprint(device *kandji.Device) bool {
	includeIDs := createSet(s.config.Kandji.BlueprintsInclude.BlueprintIDs)
	includeNames := createSet(s.config.Kandji.BlueprintsInclude.BlueprintNames)
	excludeIDs := createSet(s.config.Kandji.BlueprintsExclude.BlueprintIDs)
	excludeNames := createSet(s.config.Kandji.BlueprintsExclude.BlueprintNames)

	// Log device blueprint info for debugging
	s.log.Debug("Checking device blueprint",
		"serial_number", device.SerialNumber,
		"device_blueprint_id", device.BlueprintID,
		"device_blueprint_name", device.BlueprintName,
		"include_ids", includeIDs,
		"blueprint_names", includeNames)

	// Exclude filter has priority
	if _, ok := excludeIDs[device.BlueprintID]; ok {
		s.log.Debug("Device excluded by blueprint ID", "serial_number", device.SerialNumber, "blueprint_id", device.BlueprintID)
		return false
	}
	if _, ok := excludeNames[device.BlueprintName]; ok {
		s.log.Debug("Device excluded by blueprint name", "serial_number", device.SerialNumber, "blueprint_name", device.BlueprintName)
		return false
	}

	// If no include filters are set, all non-excluded devices are included.
	if len(includeIDs) == 0 && len(includeNames) == 0 {
		return true
	}

	// Include filter
	if _, ok := includeIDs[device.BlueprintID]; ok {
		s.log.Debug("Device included by blueprint ID", "serial_number", device.SerialNumber, "blueprint_id", device.BlueprintID)
		return true
	}
	if _, ok := includeNames[device.BlueprintName]; ok {
		s.log.Debug("Device included by blueprint name", "serial_number", device.SerialNumber, "blueprint_name", device.BlueprintName)
		return true
	}

	s.log.Debug("Device did not match any include blueprint filters", "serial_number", device.SerialNumber, "device_blueprint_name", device.BlueprintName, "device_blueprint_id", device.BlueprintID, "include_ids", includeIDs, "blueprint_names", includeNames)
	return false
}

// deviceHasAnyTag checks if a device has any of the specified tags
func (s *Syncer) deviceHasAnyTag(device kandji.Device, includeTags []string) bool {
	for _, deviceTag := range device.Tags {
		for _, includeTag := range includeTags {
			if deviceTag == includeTag {
				return true
			}
		}
	}
	return false
}
