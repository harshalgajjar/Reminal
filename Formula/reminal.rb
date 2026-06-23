class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.7.10"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.10/reminal_0.7.10_darwin_arm64.tar.gz"
      sha256 "cc1e909569b37a3334325917fbe164cc6cb316a12009e5e04999847ac94272bd"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.10/reminal_0.7.10_darwin_amd64.tar.gz"
      sha256 "a85a8675712d2ea63250ac54151bb385b5c03ec61d8a537d6f75b6a1e62533a1"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.10/reminal_0.7.10_linux_arm64.tar.gz"
      sha256 "8c7a7005349320a6f9a501eba09306e4a36bd6c28881e22cc7c11177792a5c9d"
    end
  end

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      system "go", "build", "-ldflags=#{ldflags}", "-o", bin/"reminal", "./cmd/reminal"
    else
      bin.install "reminal"
    end
  end

  def ldflags
    "-s -w " \
      "-X main.version=#{version} " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.futuristic.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.futuristic.workers.dev"
  end

  def caveats
    <<~EOS
      reminal connects to the hosted relay automatically — no setup needed.

        reminal              # share your terminal
        reminal --connect ID --pin PIN
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/reminal version")
  end
end
