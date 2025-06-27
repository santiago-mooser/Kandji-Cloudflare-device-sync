# Kandji-Cloudflare Device Syncer

A service that automatically synchronizes device information from Kandji to Cloudflare Zero Trust lists, enabling device-based access controls through Cloudflare Gateway and WARP.

## Overview

This syncer pulls device serial numbers from your Kandji MDM and maintains them in a Cloudflare list that can be used for Zero Trust policies. This enables you to:

- Automatically whitelist managed devices in Cloudflare Gateway
- Create device-based access policies using WARP
- Maintain an up-to-date inventory of corporate devices for network access control
- Ensure only Kandji-managed devices can access your resources through Cloudflare

## Features

- **Automated Sync**: Continuously synchronizes device data between Kandji and Cloudflare
- **Device Filtering**: Filter devices by platform, ownership, and custom tags
- **Batch Processing**: Efficient bulk operations for large device inventories
- **Rate Limiting**: Respects API rate limits for both Kandji and Cloudflare
- **Missing Device Handling**: Configurable actions for devices removed from Kandji
- **Zero Trust Integration**: Direct integration with Cloudflare's Zero Trust platform
- **Comprehensive Logging**: Structured JSON logging with configurable levels

## Prerequisites

- Kandji MDM instance with API access
- Cloudflare account with Zero Trust subscription
- Go 1.23+ (for building from source)

## Installation

### From Source

```bash
git clone <repository-url>
cd kandji-cloudflare-device-syncer
go build -o kandji-cloudflare-syncer .
```

### Configuration

1. Copy the example configuration:
```bash
cp config.example.yaml config.yaml
```

2. Edit `config.yaml` with your settings or use environment variables (recommended):

```bash
export KANDJI_API_URL="https://your-tenant.api.kandji.io"
export KANDJI_API_TOKEN="your-kandji-api-token"
export CLOUDFLARE_API_TOKEN="your-cloudflare-api-token"
export CLOUDFLARE_ACCOUNT_ID="your-cloudflare-account-id"
export CLOUDFLARE_LIST_ID="your-cloudflare-list-id"
```

## Cloudflare Setup

### 1. Create API Token

1. Go to Cloudflare Dashboard > My Profile > API Tokens
2. Click "Create Token"
3. Use "Custom token" template
4. Set permissions:
   - Account: `Zone:Read`, `Account:Read`
   - Zone Resources: Include `All zones`
   - Account Resources: Include `All accounts`
   - Specific permissions: `List:Edit`

### 2. Create Zero Trust List

1. Go to Zero Trust Dashboard > Lists
2. Click "Create list"
3. Choose **SERIAL** list type (not "Generic") for device serial numbers
4. Name it (e.g., "Kandji Managed Devices")
5. Copy the List ID for your configuration

### 3. Create Gateway Policy (Optional)

1. Go to Zero Trust > Gateway > Firewall policies
2. Create new policy
3. Set condition: "Device Serial Number is in [Your List Name]"
4. Set action: "Allow"
5. This will automatically allow devices synced from Kandji

## Configuration Options

### Core Settings

- `sync_interval`: How often to run the sync (e.g., `5m`, `1h`)
- `on_missing`: Action for devices in Cloudflare but not in Kandji (`ignore`, `delete`, `alert`)
- `sync_devices_without_owners`: Include devices without assigned users

### Device Filtering

- `include_tags`: Only sync devices with these tags (empty = all devices)
- `exclude_tags`: Skip devices with these tags
- Platform filtering: Automatically excludes iPhone and iPad devices
- `blueprints_include` / `blueprints_exclude`: Filter devices by Kandji blueprint IDs or names
- `sync_mobile_devices`: If true, syncs mobile devices (default: false)

### Performance Tuning

- `rate_limits`: Configure API request rates
- `batch.size`: Number of devices per batch operation
- `sync_interval`: How often to run the sync process (e.g., 5m, 1h, 30s)

## Usage

### Basic Usage

```bash
./kandji-cloudflare-syncer
```

- By default, the app loads `config.yaml` from the current directory.
- You can override config file location with `-config` flag.

### With Custom Config

```bash
./kandji-cloudflare-syncer -config custom-config.yaml
```

### Check Version

```bash
./kandji-cloudflare-syncer -version
```

## Device Synchronization Logic

1. **Fetch Devices**: Retrieves devices from both Kandji and Cloudflare list
2. **Apply Filters**:
   - Removes iPhone/iPad devices
   - Applies ownership filters
   - Applies tag-based include/exclude filters
3. **Calculate Differences**: Identifies new devices and missing devices
4. **Sync Changes**:
   - Adds new devices to Cloudflare list
   - Handles missing devices per configuration
5. **Log Results**: Reports sync statistics

## Zero Trust Integration

### Device-Based Policies

Use the synced device list in Cloudflare Gateway policies:

```yaml
# Example Gateway rule
name: "Allow Managed Devices"
conditions:
  - device_serial_number in managed_devices_list
action: allow
```

### WARP Client Integration

When combined with WARP clients:

1. WARP identifies device by serial number
2. Gateway checks if serial is in the Kandji-synced list
3. Access granted/denied based on management status

## Monitoring and Logging

### Log Levels

- `debug`: Detailed operation logs
- `info`: General operational information
- `warn`: Non-fatal issues
- `error`: Error conditions

### Key Metrics

The syncer logs important metrics each cycle:

- Total devices in Kandji
- Devices after filtering
- New devices added
- Devices removed
- API errors and rate limiting

### Sample Log Output

```json
{
  "time": "2025-01-15T10:30:00Z",
  "level": "INFO",
  "msg": "Sync cycle complete",
  "kandji_devices_total": 1250,
  "eligible_devices": 1180,
  "new_devices_found": 5,
  "successfully_added": 5,
  "deleted_devices": 2
}
```

## Troubleshooting

### Common Issues

**Authentication Errors**
- Verify API tokens have correct permissions
- Check token expiration dates
- Ensure account/list IDs are correct

**Rate Limiting**
- Reduce `requests_per_second` values
- Increase `sync_interval` for less frequent runs
- Monitor API usage in respective dashboards

**Missing Devices**
- Check device filtering configuration
- Verify devices have serial numbers in Kandji
- Review include/exclude tag filters

### Debug Mode

Enable debug logging for detailed troubleshooting:

```yaml
log:
  level: "debug"
```

## Security Considerations

### API Token Security
- Use environment variables for tokens
- Restrict token permissions to minimum required
- Rotate tokens regularly
- Monitor token usage

### Configuration Security
- Set restrictive file permissions: `chmod 600 config.yaml`
- Store configuration in secure locations
- Avoid committing secrets to version control

### Network Security
- Run syncer in secure environment
- Use TLS for all API communications
- Consider VPN/private network deployment

## Performance Guidelines

### Recommended Settings

For small deployments (< 500 devices):
```yaml
sync_interval: 5m
batch.size: 50
rate_limits.cloudflare_requests_per_second: 4
```

For large deployments (> 2000 devices):
```yaml
sync_interval: 15m
batch.size: 100
rate_limits.cloudflare_requests_per_second: 2
```

### API Rate Limits

- **Kandji**: 10 requests/second (default)
- **Cloudflare**: 4 requests/second (recommended for stability)

## Contributing

1. Fork the repository
2. Create feature branch
3. Make changes with tests
4. Submit pull request

## License

See [LICENSE](LICENSE) for details.

## Support

- Check logs for error details
- Review Cloudflare and Kandji API documentation
- Open GitHub issues for bugs or feature requests
