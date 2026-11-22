require 'minitest/autorun'
require 'tmpdir'
require 'fileutils'
require_relative 'publishing'

class PublishingTest < Minitest::Test
  Build = Struct.new(:version)
  Version = Struct.new(:version_string, :app_version_state, :release_type, :build)
  BetaGroup = Struct.new(:id, :name, :is_internal_group)

  def setup
    @dir = Dir.mktmpdir
    @previous = ENV['IOS_SOURCE_DIR']
    ENV['IOS_SOURCE_DIR'] = @dir
    File.write(File.join(@dir, 'version.properties'), "version=0.56.2\nandroidVersionCode=163\n")
  end

  def teardown
    ENV['IOS_SOURCE_DIR'] = @previous
    FileUtils.remove_entry(@dir)
  end

  def test_first_update_and_editable_retry
    assert_equal :publish, Publishing.decision([Version.new('0.55.0', 'READY_FOR_DISTRIBUTION', 'MANUAL')])
    assert_equal :publish, Publishing.decision([Version.new('0.56.2', 'PREPARE_FOR_SUBMISSION', 'AFTER_APPROVAL')])
    assert_equal '163', Publishing.build_number
  end

  def test_submitted_or_live_automatic_version_is_idempotent
    %w[WAITING_FOR_REVIEW IN_REVIEW READY_FOR_DISTRIBUTION].each do |state|
      assert_equal :done, Publishing.decision([Version.new('0.56.2', state, 'AFTER_APPROVAL', Build.new('163'))])
    end
  end

  def test_never_withdraws_previous_review_or_overwrites_newer_version
    %w[PREPARE_FOR_SUBMISSION PENDING_DEVELOPER_RELEASE].each do |state|
      assert_raises(RuntimeError) { Publishing.decision([Version.new('0.55.0', state, 'MANUAL')]) }
    end
    # A previous version Apple is still handling is waited for, neither withdrawn nor a failure.
    %w[WAITING_FOR_REVIEW IN_REVIEW PENDING_APPLE_RELEASE PROCESSING_FOR_DISTRIBUTION].each do |state|
      assert_equal :waiting, Publishing.decision([Version.new('0.55.0', state, 'AFTER_APPROVAL')])
    end
    assert_raises(RuntimeError) { Publishing.decision([Version.new('0.57.0', 'READY_FOR_DISTRIBUTION', 'AFTER_APPROVAL')]) }
  end

  def test_manual_release_is_not_mistaken_for_success
    assert_raises(RuntimeError) { Publishing.decision([Version.new('0.56.2', 'PENDING_DEVELOPER_RELEASE', 'MANUAL')]) }
  end

  def test_publication_requires_exact_clean_annotated_tag
    Publishing.git('init', '-q')
    Publishing.git('config', 'user.name', 'Release test')
    Publishing.git('config', 'user.email', 'release@example.invalid')
    Publishing.git('add', 'version.properties')
    Publishing.git('commit', '-qm', 'Release fixture')
    Publishing.git('tag', 'v0.56.2')
    assert_raises(RuntimeError) { Publishing.validate_source! }
    Publishing.git('tag', '-d', 'v0.56.2')
    Publishing.git('tag', '-a', 'v0.56.2', '-m', 'Improved chat scrolling')
    Publishing.validate_source!
    assert_equal 'Improved chat scrolling', Publishing.notes
    File.write(File.join(@dir, 'untracked'), 'dirty')
    assert_raises(RuntimeError) { Publishing.validate_source! }
  end


  def test_wrong_submitted_build_is_not_reported_as_success
    version = Version.new('0.56.2', 'WAITING_FOR_REVIEW', 'AFTER_APPROVAL', Build.new('2'))
    assert_raises(RuntimeError) { Publishing.decision([version]) }
  end


  def test_invalid_credentials_cannot_appear_in_public_error_logs
    name = 'IOS_PUBLISH_TEST_SECRET'
    ENV[name] = '{"key":"credential-sentinel"'
    error = assert_raises(RuntimeError) { Publishing.secret(name, %w[key]) }
    refute_includes error.message, 'credential-sentinel'
    assert_nil error.cause
    ENV[name] = '"credential-sentinel"'
    error = assert_raises(RuntimeError) { Publishing.secret(name, %w[key]) }
    refute_includes error.message, 'credential-sentinel'
    ENV[name] = '{"key":"credential-sentinel"}'
    assert_equal 'credential-sentinel', Publishing.secret(name, %w[key]).fetch('key')
  ensure
    ENV.delete(name)
  end


  def test_only_the_app_release_uses_the_profile_and_the_tag_is_restored_on_error
    project_path = File.join(@dir, 'ios/RemoteAgent.xcodeproj')
    FileUtils.mkdir_p(File.dirname(project_path))
    project = Xcodeproj::Project.new(project_path)
    project.new_target(:application, 'RemoteAgent', :ios, '17.0')
    project.new_target(:bundle, 'PackageResources', :ios, '17.0')
    project.save
    Publishing.git('init', '-q')
    Publishing.git('config', 'user.name', 'Release test')
    Publishing.git('config', 'user.email', 'release@example.invalid')
    Publishing.git('add', '.')
    Publishing.git('commit', '-qm', 'Signing fixture')
    Publishing.git('tag', '-a', 'v0.56.2', '-m', 'Signing fixture')
    path = File.join(project_path, 'project.pbxproj')
    original = File.binread(path)
    previous = ENV['RA_SOURCE_COMMIT']
    ENV['RA_SOURCE_COMMIT'] = 'previous-value'
    Publishing.with_app_signing('EXAMPLETEAM', 'Example App Store') do
      configured = Xcodeproj::Project.open(project_path)
      configured.targets.each do |target|
        target.build_configurations.each do |config|
          if target.name == 'RemoteAgent' && config.name == 'Release'
            assert_equal 'Example App Store', config.build_settings['PROVISIONING_PROFILE_SPECIFIER']
            assert_equal 'Apple Distribution', config.build_settings['CODE_SIGN_IDENTITY']
          else
            assert_nil config.build_settings['PROVISIONING_PROFILE_SPECIFIER']
          end
        end
      end
      assert_equal Publishing.git('rev-parse', '--short=12', 'HEAD'), ENV['RA_SOURCE_COMMIT']
    end
    assert_equal original, File.binread(path)
    assert_equal 'previous-value', ENV['RA_SOURCE_COMMIT']
    assert_raises(RuntimeError) { Publishing.with_app_signing('EXAMPLETEAM', 'Example App Store') { raise 'Build failure' } }
    assert_equal original, File.binread(path)
    assert_equal 'previous-value', ENV['RA_SOURCE_COMMIT']
    Publishing.validate_source!
  ensure
    ENV['RA_SOURCE_COMMIT'] = previous
  end


  def test_release_notes_use_actual_store_locales_instead_of_the_default_pseudo_locale
    expected = { 'zh-Hans' => 'Release notes', 'en-US' => 'Release notes' }
    assert_equal expected, Publishing.localized_notes(['zh-Hans', 'en-US'], 'zh-Hans', 'Release notes')
    assert_equal({ 'zh-Hans' => 'Notes' }, Publishing.localized_notes([], 'zh-Hans', 'Notes'))
    assert_raises(RuntimeError) { Publishing.localized_notes(['default'], nil, 'Notes') }
  end

  def test_next_patch_version_increments_patch_and_rejects_malformed_versions
    assert_equal '0.56.3', Publishing.next_patch_version
    assert_equal '0.97.2', Publishing.next_patch_version('0.97.1')
    assert_equal '1.0.10', Publishing.next_patch_version('1.0.9')
    %w[01.2.3 0.97 v0.97.1 0.97.1-beta].each do |bad|
      assert_raises(RuntimeError) { Publishing.next_patch_version(bad) }
    end
  end

  def test_beta_build_number_is_monotonic_within_train_and_below_next_release
    assert_equal '163.57', Publishing.beta_build_number('57', run_attempt: '1')
    assert_equal '163.57', Publishing.beta_build_number('57', run_attempt: nil)
    assert_equal '163.57.2', Publishing.beta_build_number('57', run_attempt: '2')
    assert_equal '320.1', Publishing.beta_build_number('1', run_attempt: '1', base: '320')

    # Ordering within the train: 320.57 < 320.57.2 < 320.58 < 321 (next official release)
    ordered = [
      Publishing.beta_build_number('57', run_attempt: '1', base: '320'),
      Publishing.beta_build_number('57', run_attempt: '2', base: '320'),
      Publishing.beta_build_number('58', run_attempt: '1', base: '320'),
      '321'
    ].map { |v| Gem::Version.new(v) }
    assert_equal ordered, ordered.sort
    assert_equal ordered.size, ordered.uniq.size

    # Rejects missing/zero/non-numeric inputs and Apple's 18-character limit
    assert_raises(RuntimeError) { Publishing.beta_build_number(nil) }
    assert_raises(RuntimeError) { Publishing.beta_build_number('0') }
    assert_raises(RuntimeError) { Publishing.beta_build_number('01') }
    assert_raises(RuntimeError) { Publishing.beta_build_number('57', run_attempt: '0') }
    assert_raises(RuntimeError) { Publishing.beta_build_number('1234567890123456', base: '320') }
  end

  def test_beta_source_validation_allows_untagged_clean_commit_and_rejects_dirty_tree
    Publishing.git('init', '-q')
    Publishing.git('config', 'user.name', 'Release test')
    Publishing.git('config', 'user.email', 'release@example.invalid')
    Publishing.git('add', 'version.properties')
    Publishing.git('commit', '-qm', 'Mainline commit without tag')

    assert_raises(RuntimeError) { Publishing.validate_source! }
    Publishing.validate_source!(require_tag: false)

    File.write(File.join(@dir, 'untracked'), 'dirty')
    assert_raises(RuntimeError) { Publishing.validate_source!(require_tag: false) }
  end

  def test_with_app_signing_sets_and_restores_beta_version_env
    project_path = File.join(@dir, 'ios/RemoteAgent.xcodeproj')
    FileUtils.mkdir_p(File.dirname(project_path))
    project = Xcodeproj::Project.new(project_path)
    project.new_target(:application, 'RemoteAgent', :ios, '17.0')
    project.save
    Publishing.git('init', '-q')
    Publishing.git('config', 'user.name', 'Release test')
    Publishing.git('config', 'user.email', 'release@example.invalid')
    Publishing.git('add', '.')
    Publishing.git('commit', '-qm', 'Untagged main commit')

    prev_ver = ENV['RA_APP_VERSION']
    ENV['RA_APP_VERSION'] = 'old-ver'

    Publishing.with_app_signing('EXAMPLETEAM', 'Example App Store',
                                require_tag: false,
                                app_version: '0.56.3') do
      assert_equal '0.56.3', ENV['RA_APP_VERSION']
    end
    assert_equal 'old-ver', ENV['RA_APP_VERSION']
  ensure
    ENV['RA_APP_VERSION'] = prev_ver
  end

  def test_verify_archive_checks_version_build_commit_and_unconditional_encryption_flag
    Publishing.git('init', '-q')
    Publishing.git('config', 'user.name', 'Release test')
    Publishing.git('config', 'user.email', 'release@example.invalid')
    Publishing.git('add', 'version.properties')
    Publishing.git('commit', '-qm', 'Archive fixture')
    commit = Publishing.git('rev-parse', '--short=12', 'HEAD')

    output = File.join(@dir, 'out')
    app_dir = File.join(output, 'RemoteAgent.xcarchive/Products/Applications/RemoteAgent.app')
    FileUtils.mkdir_p(app_dir)
    write_info_plist = lambda do |hash|
      plist = CFPropertyList::List.new
      plist.value = CFPropertyList.guess(hash)
      plist.save(File.join(app_dir, 'Info.plist'), CFPropertyList::List::FORMAT_XML)
    end

    # Official release path also requires ITSAppUsesNonExemptEncryption == false
    release_valid = {
      'CFBundleIdentifier' => Publishing::BUNDLE_ID,
      'CFBundleShortVersionString' => '0.56.2',
      'CFBundleVersion' => '163',
      'RASourceCommit' => commit,
      'ITSAppUsesNonExemptEncryption' => false
    }
    write_info_plist.call(release_valid)
    info = Publishing.verify_archive!(output, verify_signature: false)
    assert_equal '0.56.2', info['CFBundleShortVersionString']
    assert_equal false, info['ITSAppUsesNonExemptEncryption']

    # Missing ITSAppUsesNonExemptEncryption must fail even on official release path
    missing_encryption = release_valid.dup
    missing_encryption.delete('ITSAppUsesNonExemptEncryption')
    write_info_plist.call(missing_encryption)
    assert_raises(RuntimeError) { Publishing.verify_archive!(output, verify_signature: false) }

    # Beta path checks injected next patch version, beta build number, commit, and encryption flag
    beta_valid = release_valid.merge('CFBundleShortVersionString' => '0.56.3', 'CFBundleVersion' => '163.57')
    write_info_plist.call(beta_valid)
    beta_info = Publishing.verify_archive!(output,
                                           expected_version: '0.56.3',
                                           expected_build_number: '163.57',
                                           verify_signature: false)
    assert_equal '0.56.3', beta_info['CFBundleShortVersionString']
    assert_equal '163.57', beta_info['CFBundleVersion']

    # Mismatched version, build number, or encryption flag must fail
    write_info_plist.call(beta_valid.merge('CFBundleShortVersionString' => '0.56.2'))
    assert_raises(RuntimeError) do
      Publishing.verify_archive!(output, expected_version: '0.56.3', expected_build_number: '163.57', verify_signature: false)
    end

    write_info_plist.call(beta_valid.merge('CFBundleVersion' => '163'))
    assert_raises(RuntimeError) do
      Publishing.verify_archive!(output, expected_version: '0.56.3', expected_build_number: '163.57', verify_signature: false)
    end

    write_info_plist.call(beta_valid.merge('ITSAppUsesNonExemptEncryption' => true))
    assert_raises(RuntimeError) do
      Publishing.verify_archive!(output, expected_version: '0.56.3', expected_build_number: '163.57', verify_signature: false)
    end
  end

  def test_internal_beta_group_name_and_resolution
    assert_equal 'Internal', Publishing.internal_beta_group_name(nil)
    assert_equal 'Internal', Publishing.internal_beta_group_name('')
    assert_equal 'Internal', Publishing.internal_beta_group_name("  \t ")
    assert_equal 'QA Team', Publishing.internal_beta_group_name('  QA Team  ')

    internal = BetaGroup.new('grp-1', 'Internal', true)
    other_internal = BetaGroup.new('grp-2', 'Core Devs', true)
    external = BetaGroup.new('grp-3', 'External', false)
    external_same_name = BetaGroup.new('grp-4', 'Internal', false)

    # Resolves internal group by name or ID, ignoring external groups even with identical name
    assert_equal internal, Publishing.resolve_internal_beta_group([external, external_same_name, internal, other_internal], 'Internal')
    assert_equal other_internal, Publishing.resolve_internal_beta_group([internal, other_internal], 'grp-2')

    # Rejects missing group, external-only group, or ambiguous duplicates
    assert_raises(RuntimeError) { Publishing.resolve_internal_beta_group([external_same_name], 'Internal') }
    assert_raises(RuntimeError) { Publishing.resolve_internal_beta_group([internal], 'Missing') }
    assert_raises(RuntimeError) { Publishing.resolve_internal_beta_group([internal, BetaGroup.new('grp-dup', 'Internal', true)], 'Internal') }
  end

  def test_internal_distribution_action_and_verification
    # Already in target group and IN_BETA_TESTING -> idempotent no-op
    assert_equal :done, Publishing.internal_distribution_action('IN_BETA_TESTING', already_in_group: true)
    # In beta testing via another group, or ready for beta testing -> must link to target group
    assert_equal :add_to_group, Publishing.internal_distribution_action('IN_BETA_TESTING', already_in_group: false)
    assert_equal :add_to_group, Publishing.internal_distribution_action('READY_FOR_BETA_TESTING', already_in_group: false)
    assert_equal :wait, Publishing.internal_distribution_action('PROCESSING', already_in_group: false)

    # Blocked or unexpected states fail explicitly
    %w[MISSING_EXPORT_COMPLIANCE PROCESSING_EXCEPTION EXPIRED IN_EXPORT_COMPLIANCE_REVIEW].each do |bad|
      assert_raises(RuntimeError) { Publishing.internal_distribution_action(bad) }
    end
    assert_raises(RuntimeError) { Publishing.internal_distribution_action(nil) }

    # Final state verification requires IN_BETA_TESTING
    assert_equal true, Publishing.verify_internal_beta_testing!('IN_BETA_TESTING')
    %w[READY_FOR_BETA_TESTING MISSING_EXPORT_COMPLIANCE PROCESSING EXPIRED].each do |bad|
      assert_raises(RuntimeError) { Publishing.verify_internal_beta_testing!(bad) }
    end
    assert_raises(RuntimeError) { Publishing.verify_internal_beta_testing!(nil) }
  end

  def test_beta_changelog_lists_commits_since_release_tag_and_truncates_utf8
    Publishing.git('init', '-q')
    Publishing.git('config', 'user.name', 'Release test')
    Publishing.git('config', 'user.email', 'release@example.invalid')
    Publishing.git('add', 'version.properties')
    Publishing.git('commit', '-qm', 'Release v0.56.2')
    Publishing.git('tag', '-a', 'v0.56.2', '-m', 'Release v0.56.2')

    # On the tag commit itself (e.g. manual dispatch), falls back to recent commits on HEAD
    on_tag = Publishing.beta_changelog(limit: 5)
    assert_includes on_tag, "main @ #{Publishing.git('rev-parse', '--short=12', 'HEAD')}"
    assert_includes on_tag, 'Release v0.56.2'

    # Commits after the release tag appear in the changelog without earlier tagged commits
    File.write(File.join(@dir, 'a.txt'), '1')
    Publishing.git('add', 'a.txt')
    Publishing.git('commit', '-qm', 'ios: fix keyboard inset')
    File.write(File.join(@dir, 'b.txt'), '2')
    Publishing.git('add', 'b.txt')
    Publishing.git('commit', '-qm', 'ios: improve scrolling')

    changelog = Publishing.beta_changelog(limit: 5)
    assert_includes changelog, "main @ #{Publishing.git('rev-parse', '--short=12', 'HEAD')}"
    assert_includes changelog, 'ios: improve scrolling'
    assert_includes changelog, 'ios: fix keyboard inset'
    refute_includes changelog, 'Release v0.56.2'

    # UTF-8 byte truncation respects Apple's 4000-byte limit without splitting multibyte chars
    long_text = '中' * 2000
    truncated = Publishing.truncate_utf8_bytes(long_text, 4000)
    assert_operator truncated.bytesize, :<=, 4000
    assert truncated.valid_encoding?
    assert truncated.end_with?('...')
  end

end
