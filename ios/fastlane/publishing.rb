require 'json'
require 'open3'
require 'rubygems'
require 'xcodeproj'
require 'cfpropertylist'

module Publishing
  BUNDLE_ID = 'io.github.lewin671.remoteagent'
  EDITABLE = %w[PREPARE_FOR_SUBMISSION DEVELOPER_REJECTED REJECTED METADATA_REJECTED INVALID_BINARY].freeze
  WAITING_ON_APPLE = %w[WAITING_FOR_REVIEW IN_REVIEW PENDING_APPLE_RELEASE PROCESSING_FOR_DISTRIBUTION].freeze
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

  SEMVER = /\A(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)\z/.freeze
  POSITIVE_INT = /\A[1-9]\d*\z/.freeze
  MAX_CF_BUNDLE_VERSION_LENGTH = 18
  MAX_CHANGELOG_BYTES = 4000
  DEFAULT_INTERNAL_GROUP = 'Internal'.freeze

  def self.version
    File.read(File.join(source, 'version.properties')).match(/^version=(\d+\.\d+\.\d+)$/)[1]
  end

  def self.build_number
    # Stable across retries and monotonic across the shared product releases.
    File.read(File.join(source, 'version.properties')).match(/^androidVersionCode=(\d+)$/)[1]
  end

  def self.next_patch_version(base = version)
    match = base.to_s.match(SEMVER)
    raise "Invalid product version: #{base}" unless match
    "#{match[1]}.#{match[2]}.#{match[3].to_i + 1}"
  end

  def self.beta_build_number(run_number = ENV['GITHUB_RUN_NUMBER'], run_attempt: ENV['GITHUB_RUN_ATTEMPT'], base: build_number)
    base_str = base.to_s
    run_str = run_number.to_s
    attempt_str = run_attempt.to_s
    raise "Invalid base build number: #{base}" unless base_str.match?(POSITIVE_INT)
    raise "Invalid GITHUB_RUN_NUMBER: #{run_number}" unless run_str.match?(POSITIVE_INT)
    result = if attempt_str.empty? || attempt_str == '1'
               "#{base_str}.#{run_str}"
             else
               raise "Invalid GITHUB_RUN_ATTEMPT: #{run_attempt}" unless attempt_str.match?(POSITIVE_INT)
               "#{base_str}.#{run_str}.#{attempt_str}"
             end
    raise "Beta build number exceeds Apple's #{MAX_CF_BUNDLE_VERSION_LENGTH}-character limit: #{result}" if result.length > MAX_CF_BUNDLE_VERSION_LENGTH
    result
  end

  def self.internal_beta_group_name(raw = ENV['IOS_TESTFLIGHT_INTERNAL_GROUP'])
    name = raw.to_s.strip
    name.empty? ? DEFAULT_INTERNAL_GROUP : name
  end

  def self.resolve_internal_beta_group(groups, name = internal_beta_group_name)
    internal_groups = groups.select(&:is_internal_group)
    matches = internal_groups.select { |group| group.name == name || group.id == name }
    if matches.empty?
      available = internal_groups.map(&:name).sort.join(', ')
      available = '(none)' if available.empty?
      raise "Internal TestFlight group '#{name}' was not found (available internal groups: #{available})"
    end
    raise "Multiple internal TestFlight groups match '#{name}'" if matches.size > 1
    matches.first
  end

  def self.internal_distribution_action(internal_build_state, already_in_group: false)
    case internal_build_state
    when 'IN_BETA_TESTING'
      already_in_group ? :done : :add_to_group
    when 'READY_FOR_BETA_TESTING'
      :add_to_group
    when 'PROCESSING'
      :wait
    else
      raise "Build cannot be distributed to internal TestFlight (internalBuildState=#{internal_build_state || 'nil'})"
    end
  end

  def self.verify_internal_beta_testing!(internal_build_state)
    return true if internal_build_state == 'IN_BETA_TESTING'
    raise "Build did not reach IN_BETA_TESTING after adding to internal group (internalBuildState=#{internal_build_state || 'nil'})"
  end

  def self.validate_clean_source!
    raise 'Publishing requires a clean product checkout' unless git('status', '--porcelain').empty?
  end

  def self.validate_source!(require_tag: true)
    validate_clean_source!
    return unless require_tag
    raise 'Build must be the exact version tag' unless git('rev-parse', "v#{version}^{commit}") == git('rev-parse', 'HEAD')
    raise 'Release tag must be annotated' unless git('cat-file', '-t', "v#{version}") == 'tag'
  end

  def self.with_app_signing(team_id, profile_name, require_tag: true, app_version: nil)
    validate_source!(require_tag: require_tag)
    path = File.join(source, 'ios/RemoteAgent.xcodeproj/project.pbxproj')
    original = File.binread(path)
    previous_commit = ENV['RA_SOURCE_COMMIT']
    previous_version = ENV['RA_APP_VERSION']
    begin
      # Only build-specific signing metadata changes; restore the project byte-for-byte.
      ENV['RA_SOURCE_COMMIT'] = git('rev-parse', '--short=12', 'HEAD')
      if app_version.nil?
        ENV.delete('RA_APP_VERSION')
      else
        ENV['RA_APP_VERSION'] = app_version
      end
      project = Xcodeproj::Project.open(File.dirname(path))
      target = project.targets.find { |item| item.name == 'RemoteAgent' }
      config = target&.build_configurations&.find { |item| item.name == 'Release' }
      raise 'RemoteAgent Release configuration is missing' unless config
      config.build_settings.merge!('CODE_SIGN_STYLE' => 'Manual', 'DEVELOPMENT_TEAM' => team_id,
                                  'CODE_SIGN_IDENTITY' => 'Apple Distribution',
                                  'PROVISIONING_PROFILE_SPECIFIER' => profile_name)
      project.save
      yield
    ensure
      File.binwrite(path, original)
      ENV['RA_SOURCE_COMMIT'] = previous_commit
      ENV['RA_APP_VERSION'] = previous_version
    end
    # Unexpected changes to dependencies or sources must not reach the uploader.
    validate_source!(require_tag: require_tag)
  end

  def self.verify_archive!(output, expected_version: version, expected_build_number: build_number, verify_signature: true)
    app = File.join(output, 'RemoteAgent.xcarchive/Products/Applications/RemoteAgent.app')
    info = CFPropertyList.native_types(CFPropertyList::List.new(file: File.join(app, 'Info.plist')).value)
    expected = { 'CFBundleIdentifier' => BUNDLE_ID,
                 'CFBundleShortVersionString' => expected_version,
                 'CFBundleVersion' => expected_build_number,
                 'RASourceCommit' => git('rev-parse', '--short=12', 'HEAD'),
                 'ITSAppUsesNonExemptEncryption' => false }
    expected.each { |name, value| raise "Archive #{name} does not match the release" unless info[name] == value }
    if verify_signature
      _, _, status = Open3.capture3('codesign', '--verify', '--deep', '--strict', app)
      raise 'Archive signature validation failed' unless status.success?
    end
    info
  end

  def self.notes
    text = git('for-each-ref', '--format=%(contents)', "refs/tags/v#{version}")
    raise 'Release notes must contain 1–4000 characters' unless (1..4000).cover?(text.length)
    text
  end

  def self.beta_changelog(limit: 5)
    sha = git('rev-parse', '--short=12', 'HEAD')
    tag_ref = "refs/tags/v#{version}"
    _, tag_status = Open3.capture2('git', '-C', source, 'rev-parse', '--verify', '--quiet', "#{tag_ref}^{commit}")
    range = if tag_status.success?
              _, anc_status = Open3.capture2('git', '-C', source, 'merge-base', '--is-ancestor', "#{tag_ref}^{commit}", 'HEAD')
              anc_status.success? ? "#{tag_ref}..HEAD" : 'HEAD'
            else
              'HEAD'
            end
    subjects = git('log', "-n#{limit}", '--format=- %s (%h)', range)
    subjects = git('log', "-n#{limit}", '--format=- %s (%h)', 'HEAD') if subjects.empty?
    text = "main @ #{sha}\n\n#{subjects}".strip
    raise 'Beta changelog must not be empty' if text.empty?
    truncate_utf8_bytes(text, MAX_CHANGELOG_BYTES)
  end

  def self.truncate_utf8_bytes(text, max_bytes)
    return text if text.bytesize <= max_bytes
    ellipsis = '...'
    budget = max_bytes - ellipsis.bytesize
    out = +''
    text.each_char do |char|
      break if out.bytesize + char.bytesize > budget
      out << char
    end
    out + ellipsis
  end

  def self.localized_notes(locales, primary_locale, text)
    languages = (locales + [primary_locale]).compact.uniq.reject { |locale| locale.empty? || locale == 'default' }
    raise 'App Store localization is missing' if languages.empty?
    languages.to_h { |locale| [locale, text] }
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
      elsif WAITING_ON_APPLE.include?(state)
        # Apple has the previous version: nothing here is wrong and nothing can be done until
        # it is through. This release is not submitted; the next one after that will be.
        return :waiting
      elsif !%w[READY_FOR_DISTRIBUTION REPLACED_WITH_NEW_VERSION].include?(state)
        raise "Previous App Store version #{v.version_string} is still #{state}; finish it before retrying"
      end
    end
    :publish
  end
end
