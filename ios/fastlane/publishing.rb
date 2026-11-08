require 'json'
require 'open3'
require 'rubygems'

module Publishing
  BUNDLE_ID = 'io.github.lewin671.remoteagent'
  EDITABLE = %w[PREPARE_FOR_SUBMISSION DEVELOPER_REJECTED REJECTED METADATA_REJECTED INVALID_BINARY].freeze
  SUBMITTED = %w[WAITING_FOR_REVIEW IN_REVIEW PENDING_APPLE_RELEASE PROCESSING_FOR_DISTRIBUTION READY_FOR_DISTRIBUTION].freeze

  def self.secret(name, required)
    value = JSON.parse(ENV.fetch(name, ''))
    valid = value.is_a?(Hash) && required.all? { |key| value[key].is_a?(String) && !value[key].empty? }
    raise "Invalid #{name} configuration" unless valid
    value
  rescue JSON::ParserError
    # Parser errors can include fragments of a credential in public CI logs.
    raise "Invalid #{name} JSON configuration", cause: nil
  end

  def self.source
    File.realpath(ENV.fetch('IOS_SOURCE_DIR', File.expand_path('../..', __dir__)))
  end

  def self.git(*args)
    output, status = Open3.capture2('git', '-C', source, *args)
    raise 'git failed' unless status.success?
    output.strip
  end

  def self.version
    File.read(File.join(source, 'version.properties')).match(/^version=(\d+\.\d+\.\d+)$/)[1]
  end

  def self.build_number
    # Stable across retries and monotonic across the shared product releases.
    File.read(File.join(source, 'version.properties')).match(/^androidVersionCode=(\d+)$/)[1]
  end

  def self.validate_source!
    raise 'Publishing requires a clean product checkout' unless git('status', '--porcelain').empty?
    raise 'Build must be the exact version tag' unless git('rev-parse', "v#{version}^{commit}") == git('rev-parse', 'HEAD')
    raise 'Release tag must be annotated' unless git('cat-file', '-t', "v#{version}") == 'tag'
  end

  def self.notes
    text = git('for-each-ref', '--format=%(contents)', "refs/tags/v#{version}")
    raise 'Release notes must contain 1–4000 characters' unless (1..4000).cover?(text.length)
    text
  end

  def self.decision(versions)
    versions.sort_by { |v| Gem::Version.new(v.version_string) }.reverse.each do |v|
      number = Gem::Version.new(v.version_string)
      state = v.app_version_state
      raise "A newer App Store version exists (#{v.version_string})" if number > Gem::Version.new(version)
      if v.version_string == version
        if SUBMITTED.include?(state) && v.release_type == 'AFTER_APPROVAL'
          raise 'Submitted build number does not match the product release' unless v.build&.version == build_number
          return :done
        end
        raise "Version #{version} needs attention: #{state} / #{v.release_type}" unless EDITABLE.include?(state)
      elsif !%w[READY_FOR_DISTRIBUTION REPLACED_WITH_NEW_VERSION].include?(state)
        raise "Previous App Store version #{v.version_string} is still #{state}; finish it before retrying"
      end
    end
    :publish
  end
end
